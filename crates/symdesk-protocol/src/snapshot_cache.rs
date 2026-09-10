//! Watcher-invalidated snapshot storage with bounded retained payload bytes.
//! Cold-build allocation is not bounded here. Notifications never authorize I/O.

use std::{
    path::Path,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
};

use axum::body::Bytes;
use notify::{Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher};

const MAX_CACHE_BYTES: usize = 64 << 20;

pub(super) struct SnapshotPayload {
    pub plain: Bytes,
    pub compressed: Bytes,
    pub etag: String,
}

pub(super) struct SnapshotCache {
    payload: Mutex<Option<Arc<SnapshotPayload>>>,
    root_identity: Mutex<Option<String>>,
    dirty: Arc<AtomicBool>,
    healthy: Arc<AtomicBool>,
    #[cfg(test)]
    read_failure: AtomicBool,
    // Retain the watcher until the server drops the cache. Mutex provides Sync
    // for platform backends without exposing the watcher to request handlers.
    _watcher: Mutex<Option<RecommendedWatcher>>,
}

impl SnapshotCache {
    pub fn uncached() -> Self {
        Self {
            payload: Mutex::new(None),
            root_identity: Mutex::new(None),
            dirty: Arc::new(AtomicBool::new(true)),
            healthy: Arc::new(AtomicBool::new(false)),
            #[cfg(test)]
            read_failure: AtomicBool::new(false),
            _watcher: Mutex::new(None),
        }
    }

    #[cfg(test)]
    pub(super) fn set_healthy(&self, healthy: bool) {
        self.healthy.store(healthy, Ordering::SeqCst);
    }

    #[cfg(test)]
    pub(super) fn set_dirty(&self, dirty: bool) {
        self.dirty.store(dirty, Ordering::SeqCst);
    }

    #[cfg(test)]
    pub(super) fn is_dirty(&self) -> bool {
        self.dirty.load(Ordering::SeqCst)
    }

    #[cfg(test)]
    pub(super) fn payload(&self) -> Option<Arc<SnapshotPayload>> {
        self.payload.lock().ok().and_then(|payload| payload.clone())
    }

    #[cfg(test)]
    pub(super) fn inject_read_failure(&self) {
        self.read_failure.store(true, Ordering::SeqCst);
    }

    #[cfg(test)]
    pub(super) fn take_read_failure(&self) -> bool {
        self.read_failure.swap(false, Ordering::SeqCst)
    }

    pub fn new(root: &Path) -> Self {
        let mut cache = Self::uncached();
        let dirty = Arc::clone(&cache.dirty);
        let healthy = Arc::clone(&cache.healthy);
        let watched_root = root.to_path_buf();
        // Set health before registration so an early callback error cannot be
        // overwritten by a later successful watch() return.
        cache.healthy.store(true, Ordering::SeqCst);
        let watcher = notify::recommended_watcher(move |result: notify::Result<Event>| {
            match result {
                Ok(event) if matches!(event.kind, EventKind::Access(_)) => {}
                Ok(event) => {
                    if event.paths.iter().any(|path| path == &watched_root)
                        && matches!(
                            event.kind,
                            EventKind::Remove(_)
                                | EventKind::Modify(notify::event::ModifyKind::Name(_))
                        )
                    {
                        healthy.store(false, Ordering::SeqCst);
                    }
                    dirty.store(true, Ordering::SeqCst);
                }
                Err(_) => {
                    // Lost events / watcher failure must not leave an eternal
                    // cache hit. Fall back to the full confined read path.
                    healthy.store(false, Ordering::SeqCst);
                    dirty.store(true, Ordering::SeqCst);
                }
            }
        })
        .and_then(|mut watcher| {
            watcher.watch(root, RecursiveMode::Recursive)?;
            Ok(watcher)
        });
        match watcher {
            Ok(watcher) => cache._watcher = Mutex::new(Some(watcher)),
            Err(_) => cache.healthy.store(false, Ordering::SeqCst),
        }
        cache
    }

    pub fn get_or_build(
        &self,
        current: impl Fn() -> Option<String>,
        build: impl FnOnce() -> Result<SnapshotPayload, String>,
    ) -> Result<Arc<SnapshotPayload>, String> {
        // Serialize cold builds, rather than allocating one entire vault per
        // simultaneous request. The watcher callback never takes this lock.
        let mut cached = self
            .payload
            .lock()
            .map_err(|_| "snapshot cache lock poisoned".to_owned())?;
        // Clear before building. An event arriving during the read marks the
        // next request dirty, matching the Go oracle's no-lost-invalidation rule.
        let dirty = self.dirty.swap(false, Ordering::SeqCst);
        let current_identity = current();
        let identity_matches = self
            .root_identity
            .lock()
            .ok()
            .and_then(|identity| identity.clone())
            .zip(current_identity.clone())
            .is_some_and(|(cached, current)| cached == current);
        if cached.is_some() && !identity_matches {
            // Identity changes can precede or bypass root watcher delivery.
            // The watcher is still attached to the old root: never trust it
            // again. Rebuild on every request until the server is restarted.
            self.healthy.store(false, Ordering::SeqCst);
        }
        if self.healthy.load(Ordering::SeqCst)
            && identity_matches
            && !dirty
            && !self.dirty.load(Ordering::SeqCst)
            && let Some(payload) = cached.as_ref()
        {
            return Ok(Arc::clone(payload));
        }
        let built = match build() {
            Ok(payload) => Arc::new(payload),
            Err(error) => {
                self.dirty.store(true, Ordering::SeqCst);
                return Err(error);
            }
        };
        let built_identity = current();
        if built_identity.is_none() || built_identity != current_identity {
            self.dirty.store(true, Ordering::SeqCst);
            return Err("vault root changed during snapshot build".to_owned());
        }
        let size = built.plain.len().saturating_add(built.compressed.len());
        if size <= MAX_CACHE_BYTES {
            // The oracle preserves generated_at and compressed bytes if the
            // metadata ETag is unchanged, even after a spurious watcher event.
            if let Some(previous) = cached.as_ref()
                && previous.etag == built.etag
            {
                if let Ok(mut identity) = self.root_identity.lock() {
                    *identity = built_identity;
                }
                return Ok(Arc::clone(previous));
            }
            *cached = Some(Arc::clone(&built));
            if let Ok(mut identity) = self.root_identity.lock() {
                *identity = built_identity;
            }
        } else {
            *cached = None;
            if let Ok(mut identity) = self.root_identity.lock() {
                *identity = None;
            }
        }
        Ok(built)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs, thread,
        time::{Duration, Instant, SystemTime, UNIX_EPOCH},
    };

    fn payload(tag: &str) -> SnapshotPayload {
        SnapshotPayload {
            plain: Bytes::from(tag.to_owned()),
            compressed: Bytes::new(),
            etag: tag.to_owned(),
        }
    }

    #[test]
    fn replacement_without_notification_disables_old_watcher_cache_hits() {
        let cache = SnapshotCache::uncached();
        cache.healthy.store(true, Ordering::SeqCst);
        cache
            .get_or_build(|| Some("old".to_owned()), || Ok(payload("old")))
            .unwrap();
        cache
            .get_or_build(|| Some("new".to_owned()), || Ok(payload("new")))
            .unwrap();
        let next = cache
            .get_or_build(|| Some("new".to_owned()), || Ok(payload("edited")))
            .unwrap();
        assert_eq!(next.etag, "edited");
    }

    #[test]
    fn concurrent_invalidation_during_identity_check_prevents_hot_hit() {
        use std::sync::{Barrier, atomic::AtomicUsize};
        let cache = SnapshotCache::uncached();
        cache.healthy.store(true, Ordering::SeqCst);
        cache
            .get_or_build(|| Some("root".to_owned()), || Ok(payload("old")))
            .unwrap();
        let barrier = Barrier::new(2);
        let checks = AtomicUsize::new(0);
        thread::scope(|scope| {
            scope.spawn(|| {
                barrier.wait();
                cache.dirty.store(true, Ordering::SeqCst);
                barrier.wait();
            });
            let next = cache
                .get_or_build(
                    || {
                        if checks.fetch_add(1, Ordering::SeqCst) == 0 {
                            barrier.wait();
                            barrier.wait();
                        }
                        Some("root".to_owned())
                    },
                    || Ok(payload("new")),
                )
                .unwrap();
            assert_eq!(next.etag, "new");
        });
    }

    #[test]
    fn unchanged_snapshot_reuses_all_bytes_without_rebuilding() {
        let cache = SnapshotCache::uncached();
        cache.healthy.store(true, Ordering::SeqCst);
        let first = cache
            .get_or_build(|| Some("test".to_owned()), || Ok(payload("first")))
            .unwrap();
        let second = cache
            .get_or_build(|| Some("test".to_owned()), || panic!("unexpected rebuild"))
            .unwrap();
        assert!(Arc::ptr_eq(&first, &second));
    }

    #[test]
    fn invalidation_during_build_is_not_lost_and_errors_retry() {
        let cache = SnapshotCache::uncached();
        cache.healthy.store(true, Ordering::SeqCst);
        cache
            .get_or_build(
                || Some("test".to_owned()),
                || {
                    cache.dirty.store(true, Ordering::SeqCst);
                    Ok(payload("first"))
                },
            )
            .unwrap();
        assert!(
            cache
                .get_or_build(
                    || Some("test".to_owned()),
                    || Err("injected failure".to_owned())
                )
                .is_err()
        );
        let next = cache
            .get_or_build(|| Some("test".to_owned()), || Ok(payload("next")))
            .unwrap();
        assert_eq!(next.etag, "next");
    }

    #[test]
    fn missing_or_failed_watcher_never_trusts_a_clean_cache() {
        let cache = SnapshotCache::uncached();
        cache
            .get_or_build(|| Some("test".to_owned()), || Ok(payload("first")))
            .unwrap();
        assert_eq!(
            cache
                .get_or_build(|| Some("test".to_owned()), || Ok(payload("second")))
                .unwrap()
                .etag,
            "second"
        );
        cache.healthy.store(true, Ordering::SeqCst);
        cache.healthy.store(false, Ordering::SeqCst);
        assert_eq!(
            cache
                .get_or_build(|| Some("test".to_owned()), || Ok(payload("third")))
                .unwrap()
                .etag,
            "third"
        );
    }

    #[test]
    fn oversized_payload_is_not_retained() {
        let cache = SnapshotCache::uncached();
        cache.healthy.store(true, Ordering::SeqCst);
        let mut large = payload("large");
        large.plain = Bytes::from(vec![0; MAX_CACHE_BYTES + 1]);
        cache
            .get_or_build(|| Some("test".to_owned()), || Ok(large))
            .unwrap();
        assert!(cache.payload.lock().unwrap().is_none());
    }

    #[test]
    fn unchanged_etag_preserves_generated_payload_after_invalidation() {
        let cache = SnapshotCache::uncached();
        let first = cache
            .get_or_build(|| Some("test".to_owned()), || Ok(payload("same")))
            .unwrap();
        let second = cache
            .get_or_build(
                || Some("test".to_owned()),
                || {
                    let mut next = payload("same");
                    next.plain = Bytes::from_static(b"different generation time");
                    Ok(next)
                },
            )
            .unwrap();
        assert!(Arc::ptr_eq(&first, &second));
    }

    struct TempRoot(std::path::PathBuf);
    impl Drop for TempRoot {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn native_watcher_invalidates_external_create_write_rename_and_delete() {
        let root = TempRoot(std::env::temp_dir().join(format!(
                "symdesk-snapshot-{}-{}",
                std::process::id(),
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_nanos()
            )));
        fs::create_dir(&root.0).unwrap();
        let canonical = fs::canonicalize(&root.0).unwrap();
        let cache = SnapshotCache::new(&canonical);
        assert!(
            cache.healthy.load(Ordering::SeqCst),
            "native watcher failed to start"
        );
        let file = canonical.join("note.md");
        let renamed = canonical.join("renamed.md");
        for step in 0..4 {
            cache.dirty.store(false, Ordering::SeqCst);
            match step {
                0 => fs::write(&file, b"first").unwrap(),
                1 => fs::write(&file, b"second").unwrap(),
                2 => fs::rename(&file, &renamed).unwrap(),
                _ => fs::remove_file(&renamed).unwrap(),
            }
            let deadline = Instant::now() + Duration::from_secs(10);
            while !cache.dirty.load(Ordering::SeqCst) && Instant::now() < deadline {
                thread::sleep(Duration::from_millis(10));
            }
            assert!(
                cache.dirty.load(Ordering::SeqCst),
                "no invalidation for operation {step}"
            );
        }
    }
}
