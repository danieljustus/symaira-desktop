//! TLS front for the self-hosted server: Go `http.Server.ListenAndServeTLS`.
//!
//! Go loads the certificate pair with `tls.LoadX509KeyPair` after the listener
//! is bound, offers TLS 1.2 and 1.3 and answers a plaintext HTTP request on the
//! TLS port with a fixed 400 text. This port uses rustls with an explicit
//! `ring` provider and `with_safe_default_protocol_versions` (TLS 1.2 + 1.3).
//!
//! Declared difference: Go also negotiates HTTP/2 through ALPN; the Rust server
//! speaks HTTP/1.1 only and offers `http/1.1`, so an h2-capable client falls
//! back to HTTP/1.1 (never more permissive).

use std::{fs, io, path::Path, sync::Arc, time::Duration};

use rustls::{
    ServerConfig, crypto::ring,
    pki_types::{CertificateDer, PrivateKeyDer, pem::PemObject},
};
use tokio::io::AsyncWriteExt;
use tokio_rustls::TlsAcceptor;

/// Go's `tlsHandshakeTimeout`: the smaller non-zero of `ReadHeaderTimeout` and
/// `ReadTimeout`.
pub(crate) const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);

/// What Go writes to a plaintext client of a TLS listener
/// (`net/http` `tlsRecordHeaderLooksLikeHTTP`).
pub(crate) const PLAINTEXT_ON_TLS_REPLY: &[u8] =
    b"HTTP/1.0 400 Bad Request\r\n\r\nClient sent an HTTP request to an HTTPS server.\n";

fn read_file(path: &str) -> Result<Vec<u8>, String> {
    fs::read(path).map_err(|error| symdesk_vault::go_path_error("open", Path::new(path), &error))
}

/// Go `tls.LoadX509KeyPair` error wording for the failures a pair can have.
pub(crate) fn acceptor(cert_path: &str, key_path: &str) -> Result<TlsAcceptor, String> {
    let cert_bytes = read_file(cert_path)?;
    let key_bytes = read_file(key_path)?;
    let certs: Vec<CertificateDer<'static>> = CertificateDer::pem_slice_iter(&cert_bytes)
        .collect::<Result<_, _>>()
        .map_err(|_| "tls: failed to find any PEM data in certificate input".to_owned())?;
    if certs.is_empty() {
        return Err("tls: failed to find any PEM data in certificate input".to_owned());
    }
    let key = PrivateKeyDer::from_pem_slice(&key_bytes)
        .map_err(|_| "tls: failed to find any PEM data in key input".to_owned())?;
    let mut config = ServerConfig::builder_with_provider(Arc::new(ring::default_provider()))
        .with_safe_default_protocol_versions()
        .map_err(|error| format!("tls: {error}"))?
        .with_no_client_auth()
        .with_single_cert(certs, key)
        .map_err(|error| match error {
            rustls::Error::InconsistentKeys(_) => {
                "tls: private key does not match public key".to_owned()
            }
            other => format!("tls: {other}"),
        })?;
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(TlsAcceptor::from(Arc::new(config)))
}

/// Go's `tlsRecordHeaderLooksLikeHTTP`.
pub(crate) fn looks_like_http(header: &[u8; 5]) -> bool {
    matches!(
        header.as_slice(),
        b"GET /" | b"HEAD " | b"POST " | b"PUT /" | b"OPTIO"
    )
}

/// Reads the first five bytes without consuming them; `None` when the peer
/// closed or the handshake budget ran out.
pub(crate) async fn peek_record_header(stream: &tokio::net::TcpStream) -> Option<[u8; 5]> {
    let deadline = tokio::time::Instant::now() + HANDSHAKE_TIMEOUT;
    let mut header = [0_u8; 5];
    loop {
        match tokio::time::timeout_at(deadline, stream.peek(&mut header)).await {
            Ok(Ok(0)) | Ok(Err(_)) | Err(_) => return None,
            Ok(Ok(5)) => return Some(header),
            Ok(Ok(_)) => tokio::time::sleep(Duration::from_millis(2)).await,
        }
    }
}

pub(crate) async fn reply_plaintext_on_tls(stream: &mut tokio::net::TcpStream) -> io::Result<()> {
    stream.write_all(PLAINTEXT_ON_TLS_REPLY).await?;
    stream.shutdown().await
}
