//! Cancellation of an in-flight blocking provider call.
//!
//! Go cancels a provider request through its `context.Context`: the transport
//! closes the connection and the blocked read returns. `ureq` has no such
//! hook, so the cancellable agent owns the TCP connect: every socket it opens
//! is registered in a [`CancelToken`], and [`CancelToken::cancel`] shuts those
//! sockets down so a read blocked on a stalled provider returns at once and the
//! provider observes the disconnect. TLS (rustls) is chained on top of the same
//! registered socket, so `https` endpoints cancel the same way.

use std::{
    fmt,
    io::{self, Read, Write},
    net::{Shutdown, TcpStream},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time,
};

use ureq::{
    Error, Timeout,
    unversioned::{
        resolver::DefaultResolver,
        transport::{
            Buffers, ConnectionDetails, Connector, LazyBuffers, NextTimeout, RustlsConnector,
            Transport,
        },
    },
};

/// Shared cancellation flag plus the sockets a cancellable call opened.
#[derive(Clone, Default)]
pub struct CancelToken {
    inner: Arc<Inner>,
}

#[derive(Default)]
struct Inner {
    cancelled: AtomicBool,
    sockets: Mutex<Vec<TcpStream>>,
}

impl CancelToken {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Mark the token cancelled and close every registered socket. Idempotent.
    pub fn cancel(&self) {
        self.inner.cancelled.store(true, Ordering::SeqCst);
        if let Ok(mut sockets) = self.inner.sockets.lock() {
            for socket in sockets.drain(..) {
                let _ = socket.shutdown(Shutdown::Both);
            }
        }
    }

    #[must_use]
    pub fn is_cancelled(&self) -> bool {
        self.inner.cancelled.load(Ordering::SeqCst)
    }

    fn register(&self, socket: &TcpStream) {
        let Ok(clone) = socket.try_clone() else {
            return;
        };
        if let Ok(mut sockets) = self.inner.sockets.lock() {
            sockets.push(clone);
        }
        // A cancel racing the connect must not leave this socket blocking.
        if self.is_cancelled() {
            let _ = socket.shutdown(Shutdown::Both);
        }
    }

    /// An agent whose sockets are owned by this token. `config` carries the
    /// caller's timeouts and middleware.
    #[must_use]
    pub fn agent(&self, config: ureq::config::Config) -> ureq::Agent {
        let connector = CancelTcpConnector {
            token: self.clone(),
        }
        .chain(RustlsConnector::default());
        ureq::Agent::with_parts(config, connector, DefaultResolver::default())
    }
}

struct CancelTcpConnector {
    token: CancelToken,
}

impl fmt::Debug for CancelTcpConnector {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("CancelTcpConnector").finish()
    }
}

impl<In: Transport> Connector<In> for CancelTcpConnector {
    type Out = ureq::unversioned::transport::Either<In, CancelTcpTransport>;

    fn connect(
        &self,
        details: &ConnectionDetails,
        chained: Option<In>,
    ) -> Result<Option<Self::Out>, Error> {
        if let Some(chained) = chained {
            return Ok(Some(ureq::unversioned::transport::Either::A(chained)));
        }
        if self.token.is_cancelled() {
            return Err(Error::Io(io::Error::other("context canceled")));
        }
        let timeout = details.timeout.not_zero().map(|t| *t);
        let mut last = None;
        for address in details.addrs.iter() {
            let attempt = match timeout {
                Some(limit) => TcpStream::connect_timeout(address, limit),
                None => TcpStream::connect(address),
            };
            match attempt {
                Ok(stream) => {
                    if details.config.no_delay() {
                        stream.set_nodelay(true)?;
                    }
                    self.token.register(&stream);
                    let buffers = LazyBuffers::new(
                        details.config.input_buffer_size(),
                        details.config.output_buffer_size(),
                    );
                    return Ok(Some(ureq::unversioned::transport::Either::B(
                        CancelTcpTransport {
                            stream,
                            buffers,
                            token: self.token.clone(),
                        },
                    )));
                }
                Err(error) if error.kind() == io::ErrorKind::TimedOut => {
                    last = Some(Error::Timeout(Timeout::Connect));
                }
                Err(error) => last = Some(Error::Io(error)),
            }
        }
        Err(last.unwrap_or_else(|| {
            Error::Io(io::Error::new(
                io::ErrorKind::ConnectionRefused,
                "Connection refused",
            ))
        }))
    }
}

pub struct CancelTcpTransport {
    stream: TcpStream,
    buffers: LazyBuffers,
    token: CancelToken,
}

impl fmt::Debug for CancelTcpTransport {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("CancelTcpTransport")
            .field("addr", &self.stream.peer_addr().ok())
            .finish()
    }
}

fn io_error(error: io::Error, reason: Timeout) -> Error {
    match error.kind() {
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock => Error::Timeout(reason),
        _ => Error::Io(error),
    }
}

impl Transport for CancelTcpTransport {
    fn buffers(&mut self) -> &mut dyn Buffers {
        &mut self.buffers
    }

    fn transmit_output(&mut self, amount: usize, timeout: NextTimeout) -> Result<(), Error> {
        self.stream
            .set_write_timeout(timeout.not_zero().map(|t| *t))?;
        let output = &self.buffers.output()[..amount];
        self.stream
            .write_all(output)
            .map_err(|error| io_error(error, timeout.reason))
    }

    fn await_input(&mut self, timeout: NextTimeout) -> Result<bool, Error> {
        self.stream.set_read_timeout(timeout.not_zero().map(|t| {
            let duration: time::Duration = *t;
            duration
        }))?;
        let input = self.buffers.input_append_buf();
        let amount = self
            .stream
            .read(input)
            .map_err(|error| io_error(error, timeout.reason))?;
        self.buffers.input_appended(amount);
        Ok(amount > 0)
    }

    fn is_open(&mut self) -> bool {
        !self.token.is_cancelled()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::{Read, Write},
        net::TcpListener,
        thread,
        time::{Duration, Instant},
    };

    /// A server that answers headers plus one chunk and then stalls; the test
    /// proves cancel unblocks the reader and the server sees the disconnect.
    #[test]
    fn cancel_unblocks_a_stalled_read_and_closes_the_connection() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let server = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut buffer = [0_u8; 4096];
            let _ = stream.read(&mut buffer).unwrap();
            stream
                .write_all(b"HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\ntransfer-encoding: chunked\r\n\r\n5\r\nhello\r\n")
                .unwrap();
            stream.flush().unwrap();
            // Stalled provider: block until the client closes the socket.
            stream
                .set_read_timeout(Some(Duration::from_secs(20)))
                .unwrap();
            let started = Instant::now();
            let mut sink = [0_u8; 16];
            let closed = matches!(stream.read(&mut sink), Ok(0) | Err(_));
            (closed, started.elapsed())
        });
        let token = CancelToken::new();
        let config = ureq::Agent::config_builder()
            .http_status_as_error(false)
            .proxy(None)
            .timeout_global(Some(Duration::from_secs(30)))
            .build();
        let agent = token.agent(config);
        let mut response = agent
            .get(format!("http://{address}/"))
            .call()
            .expect("response headers");
        let mut reader = response.body_mut().as_reader();
        let mut first = [0_u8; 5];
        reader.read_exact(&mut first).unwrap();
        assert_eq!(&first, b"hello");
        let canceller = token.clone();
        let cancel = thread::spawn(move || {
            thread::sleep(Duration::from_millis(100));
            canceller.cancel();
        });
        let started = Instant::now();
        let mut rest = Vec::new();
        let _ = reader.read_to_end(&mut rest);
        assert!(
            started.elapsed() < Duration::from_secs(10),
            "cancel must unblock the stalled read"
        );
        cancel.join().unwrap();
        assert!(token.is_cancelled());
        let (closed, waited) = server.join().unwrap();
        assert!(closed, "server must observe the client disconnect");
        assert!(waited < Duration::from_secs(10));
    }
}
