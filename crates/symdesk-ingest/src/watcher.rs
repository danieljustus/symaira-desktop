//! The inbox directory watcher (`internal/ingest/internal/ingest/watcher.go`):
//! recursive watch of an inbox, per-file debounce, stability check (size and
//! modification time unchanged for the stability window), forced enqueue after
//! the maximum pending age, move into the processing directory, duplicate
//! recording and pre-queue failure handling with the `.error.json` sidecar.
//!
//! Time is injected through [`Clock`] like in Go, so the debounce logic runs
//! deterministically under [`FakeClock`]. The event source is the `notify`
//! crate where Go uses `fsnotify`.

use crate::goos;
use crate::ingest_fs::{clean_optional_dir, go_base, go_ext, go_join, is_path_within};
use crate::mail::{Log, stderr_log};
use crate::report_json::go_indent;
use crate::writer::{atomic_write_file, rfc3339_nano};
use crate::{Store, extract, pipeline};
use notify::{Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher as _};
use serde::Serialize;
use std::collections::{BTreeMap, HashMap};
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::JoinHandle;
use std::time::{Duration, SystemTime};
use time::OffsetDateTime;

/// A handle to a scheduled timer (`timer`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TimerId(pub i64);

/// `Clock`: time-dependent operations that tests can drive.
pub trait Clock: Send + Sync {
    /// Time since an arbitrary origin; only differences are used.
    fn now(&self) -> Duration;
    fn after_func(&self, d: Duration, f: Box<dyn FnOnce() + Send>) -> TimerId;
    /// Prevents a scheduled timer from firing; `true` if it had not fired.
    fn stop_timer(&self, t: TimerId) -> bool;
}

// ----------------------------------------------------------------- RealClock

struct Scheduled {
    id: i64,
    when: std::time::Instant,
    f: Box<dyn FnOnce() + Send>,
}

struct RealInner {
    state: Mutex<RealState>,
    wake: Condvar,
}

#[derive(Default)]
struct RealState {
    timers: Vec<Scheduled>,
    next_id: i64,
    shutdown: bool,
}

/// The production clock: one background thread runs due timers.
pub struct RealClock {
    inner: Arc<RealInner>,
    thread: Mutex<Option<JoinHandle<()>>>,
    origin: std::time::Instant,
}

impl RealClock {
    pub fn new() -> Self {
        let inner = Arc::new(RealInner {
            state: Mutex::new(RealState::default()),
            wake: Condvar::new(),
        });
        let worker = Arc::clone(&inner);
        let thread = std::thread::spawn(move || {
            loop {
                let mut guard = worker.state.lock().unwrap_or_else(|e| e.into_inner());
                if guard.shutdown {
                    return;
                }
                let now = std::time::Instant::now();
                let due: Vec<Scheduled> = {
                    let mut due = Vec::new();
                    let mut i = 0;
                    while i < guard.timers.len() {
                        if guard.timers[i].when <= now {
                            due.push(guard.timers.remove(i));
                        } else {
                            i += 1;
                        }
                    }
                    due
                };
                if !due.is_empty() {
                    drop(guard);
                    for t in due {
                        (t.f)();
                    }
                    continue;
                }
                let wait = guard
                    .timers
                    .iter()
                    .map(|t| t.when.saturating_duration_since(now))
                    .min()
                    .unwrap_or(Duration::from_secs(3600));
                let _ = worker.wake.wait_timeout(guard, wait);
            }
        });
        Self {
            inner,
            thread: Mutex::new(Some(thread)),
            origin: std::time::Instant::now(),
        }
    }
}

impl Default for RealClock {
    fn default() -> Self {
        Self::new()
    }
}

impl Clock for RealClock {
    fn now(&self) -> Duration {
        self.origin.elapsed()
    }

    fn after_func(&self, d: Duration, f: Box<dyn FnOnce() + Send>) -> TimerId {
        let mut st = self.inner.state.lock().unwrap_or_else(|e| e.into_inner());
        st.next_id += 1;
        let id = st.next_id;
        st.timers.push(Scheduled {
            id,
            when: std::time::Instant::now() + d,
            f,
        });
        self.inner.wake.notify_all();
        TimerId(id)
    }

    fn stop_timer(&self, t: TimerId) -> bool {
        let mut st = self.inner.state.lock().unwrap_or_else(|e| e.into_inner());
        let before = st.timers.len();
        st.timers.retain(|s| s.id != t.0);
        st.timers.len() != before
    }
}

impl Drop for RealClock {
    fn drop(&mut self) {
        {
            let mut st = self.inner.state.lock().unwrap_or_else(|e| e.into_inner());
            st.shutdown = true;
            self.inner.wake.notify_all();
        }
        if let Some(h) = self.thread.lock().unwrap_or_else(|e| e.into_inner()).take()
            && h.thread().id() != std::thread::current().id()
        {
            let _ = h.join();
        }
    }
}

// ----------------------------------------------------------------- FakeClock

struct FakeTimer {
    id: i64,
    when: Duration,
    f: Box<dyn FnOnce() + Send>,
}

/// Deterministic clock: timers fire only when [`FakeClock::advance`] reaches
/// them, synchronously and outside any lock.
#[derive(Default)]
pub struct FakeClock {
    state: Mutex<(Duration, Vec<FakeTimer>)>,
    next_id: AtomicI64,
}

impl FakeClock {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn advance(&self, d: Duration) {
        let due: Vec<FakeTimer> = {
            let mut st = self.state.lock().unwrap_or_else(|e| e.into_inner());
            st.0 += d;
            let now = st.0;
            let (due, rest): (Vec<_>, Vec<_>) = std::mem::take(&mut st.1)
                .into_iter()
                .partition(|t| t.when <= now);
            st.1 = rest;
            due
        };
        for t in due {
            (t.f)();
        }
    }
}

impl Clock for FakeClock {
    fn now(&self) -> Duration {
        self.state.lock().unwrap_or_else(|e| e.into_inner()).0
    }

    fn after_func(&self, d: Duration, f: Box<dyn FnOnce() + Send>) -> TimerId {
        let id = self.next_id.fetch_add(1, Ordering::SeqCst) + 1;
        let mut st = self.state.lock().unwrap_or_else(|e| e.into_inner());
        let when = st.0 + d;
        st.1.push(FakeTimer { id, when, f });
        TimerId(id)
    }

    fn stop_timer(&self, t: TimerId) -> bool {
        let mut st = self.state.lock().unwrap_or_else(|e| e.into_inner());
        let before = st.1.len();
        st.1.retain(|s| s.id != t.0);
        st.1.len() != before
    }
}

// ------------------------------------------------------------------- helpers

/// A cancellation token (`context.Context` for the event loop).
#[derive(Clone, Default)]
pub struct Ctx(Arc<AtomicBool>);

impl Ctx {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn cancel(&self) {
        self.0.store(true, Ordering::SeqCst);
    }
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::SeqCst)
    }
}

/// `isIgnored`.
pub fn is_ignored(path: &str) -> bool {
    let base = go_base(path);
    base.starts_with('.')
        || base.ends_with(".swp")
        || base.ends_with(".swx")
        || base.ends_with(".tmp")
        || base.ends_with('~')
}

fn file_exists(path: &str) -> bool {
    std::fs::metadata(path).is_ok()
}

/// `moveFileToDir`.
pub fn move_file_to_dir(path: &str, dir: &str) -> Result<String, String> {
    goos::mkdir_all(dir, 0o700).map_err(|e| format!("create target directory: {e}"))?;
    let base = go_base(path);
    let mut dst = go_join(dir, &base);
    let mut i = 2;
    while file_exists(&dst) {
        let ext = go_ext(&base);
        let stem = base.strip_suffix(ext).unwrap_or(&base);
        dst = go_join(dir, &format!("{stem}-{i}{ext}"));
        i += 1;
    }
    std::fs::rename(path, &dst).map_err(|e| {
        format!(
            "move {path} to {dst}: rename {path} {dst}: {}",
            goos::io_text(&e)
        )
    })?;
    Ok(dst)
}

#[derive(Serialize)]
struct FailureSidecar<'a> {
    source_path: &'a str,
    stage: &'a str,
    error: &'a str,
    failed_at: String,
}

/// `writeFailureSidecar`.
pub fn write_failure_sidecar(path: &str, stage: &str, err: &str) -> Result<(), String> {
    let payload = FailureSidecar {
        source_path: path,
        stage,
        error: err,
        failed_at: rfc3339_nano(OffsetDateTime::now_utc()),
    };
    let mut data = go_indent(&payload)?.into_bytes();
    data.push(b'\n');
    atomic_write_file(&format!("{path}.error.json"), &data, 0o600)
}

// ------------------------------------------------------------------- watcher

#[derive(Clone, Default)]
pub struct WatcherOptions {
    /// Zero means the one-second default.
    pub stable_for: Duration,
    pub processing_dir: String,
    pub processed_dir: String,
    pub failed_dir: String,
    pub clock: Option<Arc<dyn Clock>>,
}

struct FileState {
    last_size: u64,
    last_mod_time: Option<SystemTime>,
    created_at: Duration,
    timer: Option<TimerId>,
}

struct Inner {
    store: Arc<Mutex<Store>>,
    inbox_dir: String,
    pending: Mutex<HashMap<String, FileState>>,
    stable_for: Duration,
    clock: Arc<dyn Clock>,
    processing_dir: String,
    processed_dir: String,
    failed_dir: String,
    log: Mutex<Log>,
}

const MAX_PENDING_AGE: Duration = Duration::from_secs(5 * 60);

pub struct Watcher {
    inner: Arc<Inner>,
    fs_watcher: Arc<Mutex<Option<RecommendedWatcher>>>,
    event_thread: Mutex<Option<JoinHandle<()>>>,
}

impl Watcher {
    /// `NewWatcher`.
    pub fn new(store: Arc<Mutex<Store>>, inbox_dir: &str) -> Result<Self, String> {
        Self::with_options(store, inbox_dir, WatcherOptions::default())
    }

    /// `NewWatcherWithOptions`.
    pub fn with_options(
        store: Arc<Mutex<Store>>,
        inbox_dir: &str,
        opts: WatcherOptions,
    ) -> Result<Self, String> {
        let inbox_dir = crate::ingest_fs::go_clean(inbox_dir);
        let stable_for = if opts.stable_for.is_zero() {
            Duration::from_secs(1)
        } else {
            opts.stable_for
        };
        let clock = opts.clock.unwrap_or_else(|| Arc::new(RealClock::new()));
        let processing_dir = clean_optional_dir(&opts.processing_dir)
            .map_err(|e| format!("resolve processing dir: {e}"))?;
        let processed_dir = clean_optional_dir(&opts.processed_dir)
            .map_err(|e| format!("resolve processed dir: {e}"))?;
        let failed_dir =
            clean_optional_dir(&opts.failed_dir).map_err(|e| format!("resolve failed dir: {e}"))?;
        Ok(Self {
            inner: Arc::new(Inner {
                store,
                inbox_dir,
                pending: Mutex::new(HashMap::new()),
                stable_for,
                clock,
                processing_dir,
                processed_dir,
                failed_dir,
                log: Mutex::new(stderr_log()),
            }),
            fs_watcher: Arc::new(Mutex::new(None)),
            event_thread: Mutex::new(None),
        })
    }

    pub fn set_log(&self, log: Log) {
        *self.inner.log.lock().unwrap_or_else(|e| e.into_inner()) = log;
    }

    /// `Start`: initial recursive scan and watch setup, then the event loop on
    /// its own thread until `ctx` is cancelled.
    pub fn start(&self, ctx: &Ctx) -> Result<(), String> {
        let (tx, rx) = mpsc::channel::<notify::Result<Event>>();
        let watcher = notify::recommended_watcher(move |res| {
            let _ = tx.send(res);
        })
        .map_err(|e| format!("create fsnotify watcher: {e}"))?;
        *self.fs_watcher.lock().unwrap_or_else(|e| e.into_inner()) = Some(watcher);

        let inner = Arc::clone(&self.inner);
        let fs_watcher = Arc::clone(&self.fs_watcher);
        if let Err(e) = inner.walk_initial(ctx, &fs_watcher) {
            *self.fs_watcher.lock().unwrap_or_else(|e| e.into_inner()) = None;
            return Err(format!("initial watch setup walk: {e}"));
        }

        let inner = Arc::clone(&self.inner);
        let fs_watcher = Arc::clone(&self.fs_watcher);
        let ctx = ctx.clone();
        let handle = std::thread::spawn(move || {
            inner.event_loop(&ctx, &rx, &fs_watcher);
            *fs_watcher.lock().unwrap_or_else(|e| e.into_inner()) = None;
            let mut pending = inner.pending.lock().unwrap_or_else(|e| e.into_inner());
            for (_, state) in pending.drain() {
                if let Some(t) = state.timer {
                    inner.clock.stop_timer(t);
                }
            }
        });
        *self.event_thread.lock().unwrap_or_else(|e| e.into_inner()) = Some(handle);
        Ok(())
    }

    /// `Close`: stop watching and wait for the event loop.
    pub fn close(&self) {
        *self.fs_watcher.lock().unwrap_or_else(|e| e.into_inner()) = None;
        let handle = self
            .event_thread
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .take();
        if let Some(h) = handle {
            let _ = h.join();
        }
    }

    pub fn debounce_file(&self, ctx: &Ctx, path: &str) {
        self.inner.debounce_file(ctx, path);
    }

    pub fn cancel_debounce(&self, path: &str) {
        self.inner.cancel_debounce(path);
    }

    pub fn enqueue_file(&self, path: &str) -> Result<(), String> {
        self.inner.enqueue_file(path)
    }

    /// Pending paths with their last observed size, for tests.
    pub fn pending_snapshot(&self) -> BTreeMap<String, u64> {
        self.inner
            .pending
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .iter()
            .map(|(k, v)| (k.clone(), v.last_size))
            .collect()
    }
}

impl Drop for Watcher {
    fn drop(&mut self) {
        self.close();
    }
}

impl Inner {
    fn log(&self, msg: String) {
        let log = Arc::clone(&self.log.lock().unwrap_or_else(|e| e.into_inner()));
        log(msg);
    }

    fn store(&self) -> std::sync::MutexGuard<'_, Store> {
        self.store.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn should_ignore(&self, path: &str) -> bool {
        if is_ignored(path) {
            return true;
        }
        [&self.processing_dir, &self.processed_dir, &self.failed_dir]
            .iter()
            .any(|dir| !dir.is_empty() && dir.as_str() != "." && is_path_within(path, dir))
    }

    fn add_watch(
        &self,
        fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
        dir: &str,
    ) -> Result<(), String> {
        let mut guard = fs_watcher.lock().unwrap_or_else(|e| e.into_inner());
        match guard.as_mut() {
            Some(w) => w
                .watch(std::path::Path::new(dir), RecursiveMode::NonRecursive)
                .map_err(|e| e.to_string()),
            None => Err("watcher closed".into()),
        }
    }

    /// `filepath.WalkDir` over the inbox: lexical order, directories before
    /// their contents, symlinks not followed.
    fn walk_initial(
        self: &Arc<Self>,
        ctx: &Ctx,
        fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
    ) -> Result<(), String> {
        let meta = std::fs::symlink_metadata(&self.inbox_dir)
            .map_err(|e| goos::path_error("lstat", &self.inbox_dir, &e))?;
        self.walk_entry(ctx, fs_watcher, &self.inbox_dir.clone(), meta.is_dir())
    }

    fn walk_entry(
        self: &Arc<Self>,
        ctx: &Ctx,
        fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
        path: &str,
        is_dir: bool,
    ) -> Result<(), String> {
        if is_dir {
            if self.should_ignore(path) {
                return Ok(());
            }
            self.log(format!("[Watcher] Watching directory: {path}"));
            self.add_watch(fs_watcher, path)
                .map_err(|e| format!("watch dir {path}: {e}"))?;
            let mut names: Vec<(String, bool)> = std::fs::read_dir(path)
                .map_err(|e| goos::path_error("open", path, &e))?
                .filter_map(Result::ok)
                .map(|e| {
                    let is_dir = e.file_type().map(|t| t.is_dir()).unwrap_or(false);
                    (e.file_name().to_string_lossy().into_owned(), is_dir)
                })
                .collect();
            names.sort();
            for (name, child_is_dir) in names {
                let child = go_join(path, &name);
                self.walk_entry(ctx, fs_watcher, &child, child_is_dir)?;
            }
        } else if !self.should_ignore(path) {
            self.debounce_file(ctx, path);
        }
        Ok(())
    }

    fn watch_directory_recursive(
        self: &Arc<Self>,
        ctx: &Ctx,
        fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
        dir: &str,
    ) {
        fn walk(
            me: &Arc<Inner>,
            ctx: &Ctx,
            fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
            path: &str,
            is_dir: bool,
        ) {
            if is_dir {
                if me.should_ignore(path) {
                    return;
                }
                me.log(format!("[Watcher] Recursively watching directory: {path}"));
                let _ = me.add_watch(fs_watcher, path);
                let Ok(rd) = std::fs::read_dir(path) else {
                    return;
                };
                let mut names: Vec<(String, bool)> = rd
                    .filter_map(Result::ok)
                    .map(|e| {
                        (
                            e.file_name().to_string_lossy().into_owned(),
                            e.file_type().map(|t| t.is_dir()).unwrap_or(false),
                        )
                    })
                    .collect();
                names.sort();
                for (name, child_is_dir) in names {
                    walk(me, ctx, fs_watcher, &go_join(path, &name), child_is_dir);
                }
            } else if !me.should_ignore(path) {
                me.debounce_file(ctx, path);
            }
        }
        walk(self, ctx, fs_watcher, dir, true);
    }

    fn event_loop(
        self: &Arc<Self>,
        ctx: &Ctx,
        rx: &mpsc::Receiver<notify::Result<Event>>,
        fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
    ) {
        loop {
            if ctx.is_cancelled() {
                self.log("[Watcher] Shutting down event loop...".into());
                return;
            }
            match rx.recv_timeout(Duration::from_millis(20)) {
                Ok(Ok(event)) => self.handle_event(ctx, fs_watcher, &event),
                Ok(Err(e)) => self.log(format!("[Watcher] fsnotify error: {e}")),
                Err(RecvTimeoutError::Timeout) => {}
                Err(RecvTimeoutError::Disconnected) => return,
            }
        }
    }

    fn handle_event(
        self: &Arc<Self>,
        ctx: &Ctx,
        fs_watcher: &Arc<Mutex<Option<RecommendedWatcher>>>,
        event: &Event,
    ) {
        use notify::event::{ModifyKind, RenameMode};
        for (i, path) in event.paths.iter().enumerate() {
            let p = path.to_string_lossy().into_owned();
            if self.should_ignore(&p) {
                continue;
            }
            // Classify like fsnotify: Create / Write / Remove / Rename.
            let (create, write, remove) = match &event.kind {
                EventKind::Create(_) => (true, false, false),
                EventKind::Remove(_) => (false, false, true),
                EventKind::Modify(ModifyKind::Name(RenameMode::From)) => (false, false, true),
                EventKind::Modify(ModifyKind::Name(RenameMode::To)) => (true, false, false),
                EventKind::Modify(ModifyKind::Name(RenameMode::Both)) => {
                    if i == 0 {
                        (false, false, true)
                    } else {
                        (true, false, false)
                    }
                }
                EventKind::Modify(ModifyKind::Name(_)) => {
                    if std::fs::symlink_metadata(&p).is_ok() {
                        (true, false, false)
                    } else {
                        (false, false, true)
                    }
                }
                EventKind::Modify(_) => (false, true, false),
                EventKind::Any | EventKind::Other => {
                    if std::fs::symlink_metadata(&p).is_ok() {
                        (false, true, false)
                    } else {
                        (false, false, true)
                    }
                }
                EventKind::Access(_) => continue,
            };
            if create
                && let Ok(meta) = std::fs::symlink_metadata(&p)
                && meta.is_dir()
            {
                self.log(format!(
                    "[Watcher] New directory detected, adding to watch: {p}"
                ));
                self.watch_directory_recursive(ctx, fs_watcher, &p);
                continue;
            }
            if create || write {
                self.debounce_file(ctx, &p);
            }
            if remove {
                self.cancel_debounce(&p);
            }
        }
    }

    fn debounce_file(self: &Arc<Self>, ctx: &Ctx, path: &str) {
        let mut pending = self.pending.lock().unwrap_or_else(|e| e.into_inner());
        let Ok(info) = std::fs::metadata(path) else {
            if let Some(state) = pending.remove(path)
                && let Some(t) = state.timer
            {
                self.clock.stop_timer(t);
            }
            return;
        };
        let size = info.len();
        let mtime = info.modified().ok();
        match pending.get_mut(path) {
            Some(state) => {
                if let Some(t) = state.timer.take() {
                    self.clock.stop_timer(t);
                }
                state.last_size = size;
                state.last_mod_time = mtime;
            }
            None => {
                pending.insert(
                    path.to_owned(),
                    FileState {
                        last_size: size,
                        last_mod_time: mtime,
                        created_at: self.clock.now(),
                        timer: None,
                    },
                );
            }
        }
        let timer = self.schedule(ctx, path);
        if let Some(state) = pending.get_mut(path) {
            state.timer = Some(timer);
        }
    }

    fn schedule(self: &Arc<Self>, ctx: &Ctx, path: &str) -> TimerId {
        let me = Arc::clone(self);
        let ctx = ctx.clone();
        let path = path.to_owned();
        self.clock.after_func(
            self.stable_for,
            Box::new(move || me.check_stability(&ctx, &path)),
        )
    }

    fn check_stability(self: &Arc<Self>, ctx: &Ctx, path: &str) {
        let mut pending = self.pending.lock().unwrap_or_else(|e| e.into_inner());
        let Some(state) = pending.get(path) else {
            return;
        };
        if self.clock.now().saturating_sub(state.created_at) > MAX_PENDING_AGE {
            if let Some(state) = pending.remove(path)
                && let Some(t) = state.timer
            {
                self.clock.stop_timer(t);
            }
            drop(pending);
            self.log(format!(
                "[Watcher] File pending longer than 5m0s, force-enqueuing: {path}"
            ));
            if let Err(e) = self.enqueue_file(path) {
                self.log(format!("[Watcher] Error force-enqueuing file {path}: {e}"));
            }
            return;
        }
        let Ok(info) = std::fs::metadata(path) else {
            pending.remove(path);
            return;
        };
        if info.is_dir() {
            pending.remove(path);
            return;
        }
        let (size, mtime) = (info.len(), info.modified().ok());
        if size == state.last_size && mtime == state.last_mod_time {
            pending.remove(path);
            drop(pending);
            self.log(format!(
                "[Watcher] File is stable, submitting to queue: {path}"
            ));
            if let Err(e) = self.enqueue_file(path) {
                self.log(format!("[Watcher] Error enqueuing file {path}: {e}"));
            }
            return;
        }
        let timer = self.schedule(ctx, path);
        if let Some(state) = pending.get_mut(path) {
            state.last_size = size;
            state.last_mod_time = mtime;
            state.timer = Some(timer);
        }
    }

    fn cancel_debounce(&self, path: &str) {
        let mut pending = self.pending.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(state) = pending.remove(path)
            && let Some(t) = state.timer
        {
            self.clock.stop_timer(t);
        }
    }

    fn move_to_processing(&self, path: &str) -> Result<String, String> {
        if self.processing_dir == "." || self.processing_dir.is_empty() {
            return Ok(path.to_owned());
        }
        move_file_to_dir(path, &self.processing_dir)
    }

    fn handle_pre_queue_failure(&self, path: &str, stage: &str, err: &str) {
        if self.failed_dir == "." || self.failed_dir.is_empty() {
            return;
        }
        let failed_path = match move_file_to_dir(path, &self.failed_dir) {
            Ok(p) => p,
            Err(e) => {
                self.log(format!(
                    "[Watcher] Failed to move {path} to failed dir: {e}"
                ));
                return;
            }
        };
        if let Err(e) = write_failure_sidecar(&failed_path, stage, err) {
            self.log(format!(
                "[Watcher] Failed to write error sidecar for {failed_path}: {e}"
            ));
        }
    }

    fn enqueue_file(&self, path: &str) -> Result<(), String> {
        let work_path = self.move_to_processing(path)?;
        let log_line = |line: String| self.log(line);
        let kind = match extract::detect_file_with(&work_path, &mut |l| log_line(l)) {
            Ok(k) => k,
            Err(e) => {
                self.handle_pre_queue_failure(&work_path, "detect", &e);
                return Err(format!("detect file kind: {e}"));
            }
        };
        let hash = match pipeline::hash_file(&work_path) {
            Ok(h) => h,
            Err(e) => {
                self.handle_pre_queue_failure(&work_path, "hash", &e);
                return Err(format!("hash file: {e}"));
            }
        };
        let store = self.store();
        let (doc, created) = match store.create_or_get(&work_path, &hash, &kind, "") {
            Ok(r) => r,
            Err(e) => {
                self.handle_pre_queue_failure(&work_path, "store", &e);
                return Err(format!("create or get document: {e}"));
            }
        };
        if !created {
            self.log(format!(
                "[Watcher] File {work_path} (hash {hash}) already ingested or enqueued. Recording skipped job."
            ));
            if let Err(e) = store.enqueue_skipped_job(doc.id, &kind, "duplicate") {
                self.handle_pre_queue_failure(&work_path, "store", &e);
                return Err(format!("enqueue skipped job: {e}"));
            }
            return Ok(());
        }
        if let Err(e) = store.enqueue_job(doc.id, &kind) {
            self.handle_pre_queue_failure(&work_path, "store", &e);
            return Err(format!("enqueue job: {e}"));
        }
        Ok(())
    }
}
