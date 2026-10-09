//! Native Windows process-tree ownership for bounded index-status workers.
//!
//! The child is created suspended, assigned to a kill-on-close Job Object, and
//! only then resumed. This prevents it from creating unowned descendants in the
//! interval between process creation and job assignment.

use std::{
    io,
    mem::size_of,
    os::windows::io::AsRawHandle,
    process::Child,
    thread,
    time::{Duration, Instant},
};

use windows_sys::Win32::{
    Foundation::{CloseHandle, ERROR_NO_MORE_FILES, GetLastError, HANDLE, INVALID_HANDLE_VALUE},
    System::{
        Diagnostics::ToolHelp::{
            CreateToolhelp32Snapshot, TH32CS_SNAPTHREAD, THREADENTRY32, Thread32First, Thread32Next,
        },
        JobObjects::{
            AssignProcessToJobObject, CreateJobObjectW, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
            JOBOBJECT_BASIC_ACCOUNTING_INFORMATION, JOBOBJECT_EXTENDED_LIMIT_INFORMATION,
            JobObjectBasicAccountingInformation, JobObjectExtendedLimitInformation,
            QueryInformationJobObject, SetInformationJobObject, TerminateJobObject,
        },
        Threading::{CREATE_SUSPENDED, OpenThread, ResumeThread, THREAD_SUSPEND_RESUME},
    },
};

const CLEANUP_POLL: Duration = Duration::from_millis(5);

pub(super) struct WindowsJob {
    handle: OwnedHandle,
}

impl WindowsJob {
    pub(super) fn create() -> io::Result<Self> {
        // SAFETY: null attributes/name request a private, unnamed Job Object;
        // the returned handle is checked and owned until Drop closes it.
        let raw = unsafe { CreateJobObjectW(std::ptr::null(), std::ptr::null()) };
        let handle = OwnedHandle::new(raw)?;
        let mut limits = JOBOBJECT_EXTENDED_LIMIT_INFORMATION::default();
        limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;

        // SAFETY: `limits` has the SDK-declared ABI layout and remains alive for
        // the duration of the synchronous call; `handle` is a live Job Object.
        let configured = unsafe {
            SetInformationJobObject(
                handle.raw(),
                JobObjectExtendedLimitInformation,
                (&limits as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
            )
        };
        if configured == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self { handle })
    }

    pub(super) fn assign_and_resume(&self, child: &Child) -> io::Result<()> {
        // SAFETY: the child is still suspended and its std-owned process handle
        // and this Job Object remain live through the synchronous assignment.
        let assigned =
            unsafe { AssignProcessToJobObject(self.handle.raw(), child.as_raw_handle().cast()) };
        if assigned == 0 {
            return Err(io::Error::last_os_error());
        }
        resume_primary_thread(child.id())
    }

    /// Terminate and confirm that every process assigned to this job has exited.
    /// Closing the retained handle is an additional kill-on-close fallback.
    pub(super) fn terminate_and_wait(&self, timeout: Duration) -> io::Result<()> {
        let deadline = Instant::now() + timeout;
        let mut requested_termination = false;
        loop {
            let active = self.active_processes()?;
            if active == 0 {
                return Ok(());
            }
            if !requested_termination {
                // SAFETY: `handle` is a live Job Object. Termination is the
                // intended cleanup for this invocation-owned process tree.
                if unsafe { TerminateJobObject(self.handle.raw(), 1) } == 0 {
                    let error = io::Error::last_os_error();
                    if self.active_processes()? == 0 {
                        return Ok(());
                    }
                    return Err(error);
                }
                requested_termination = true;
            }
            if Instant::now() >= deadline {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "Windows index-status Job Object did not become empty before the cleanup deadline",
                ));
            }
            thread::sleep(CLEANUP_POLL.min(deadline.saturating_duration_since(Instant::now())));
        }
    }

    fn active_processes(&self) -> io::Result<u32> {
        let mut accounting = JOBOBJECT_BASIC_ACCOUNTING_INFORMATION::default();
        // SAFETY: `accounting` has the SDK-declared ABI layout, is writable for
        // the synchronous call, and `handle` is a live Job Object.
        let queried = unsafe {
            QueryInformationJobObject(
                self.handle.raw(),
                JobObjectBasicAccountingInformation,
                (&mut accounting as *mut JOBOBJECT_BASIC_ACCOUNTING_INFORMATION).cast(),
                size_of::<JOBOBJECT_BASIC_ACCOUNTING_INFORMATION>() as u32,
                std::ptr::null_mut(),
            )
        };
        if queried == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(accounting.ActiveProcesses)
    }

    #[cfg(test)]
    pub(super) fn reject_test_assignment(&self, child: &Child) -> io::Result<()> {
        // SAFETY: this deliberate negative test passes a null Job Object handle
        // while the child process handle is valid and owned by `Child`.
        let assigned =
            unsafe { AssignProcessToJobObject(std::ptr::null_mut(), child.as_raw_handle().cast()) };
        if assigned == 0 {
            Err(io::Error::last_os_error())
        } else {
            Err(io::Error::other(
                "invalid Job Object unexpectedly accepted a process",
            ))
        }
    }
}

struct OwnedHandle(HANDLE);

impl OwnedHandle {
    fn new(raw: HANDLE) -> io::Result<Self> {
        if raw.is_null() || raw == INVALID_HANDLE_VALUE {
            Err(io::Error::last_os_error())
        } else {
            Ok(Self(raw))
        }
    }

    fn raw(&self) -> HANDLE {
        self.0
    }
}

impl Drop for OwnedHandle {
    fn drop(&mut self) {
        // SAFETY: this wrapper takes ownership of one valid Win32 handle and
        // closes it exactly once; Job handles have kill-on-close enabled.
        unsafe {
            CloseHandle(self.0);
        }
    }
}

fn resume_primary_thread(pid: u32) -> io::Result<()> {
    // CREATE_SUSPENDED guarantees user code has not run yet, so the process has
    // only its primary thread. Assigning the process to the Job Object above
    // happens before this thread is resumed and before it can spawn children.
    let snapshot = unsafe { CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD, 0) };
    let snapshot = OwnedHandle::new(snapshot)?;
    let mut entry = THREADENTRY32 {
        dwSize: size_of::<THREADENTRY32>() as u32,
        ..THREADENTRY32::default()
    };

    // SAFETY: `entry` is writable, correctly sized, and the snapshot handle is
    // live until this function returns.
    if unsafe { Thread32First(snapshot.raw(), &mut entry) } == 0 {
        let error = unsafe { GetLastError() };
        return Err(io::Error::from_raw_os_error(error as i32));
    }
    loop {
        if entry.th32OwnerProcessID == pid {
            // SAFETY: the snapshot identifies a live thread owned by the
            // suspended child. THREAD_SUSPEND_RESUME is the only needed right.
            let thread = unsafe { OpenThread(THREAD_SUSPEND_RESUME, 0, entry.th32ThreadID) };
            let thread = OwnedHandle::new(thread)?;
            // SAFETY: `thread` is a valid handle opened with suspend/resume
            // access; this removes the single CREATE_SUSPENDED count.
            let previous_count = unsafe { ResumeThread(thread.raw()) };
            if previous_count == u32::MAX {
                return Err(io::Error::last_os_error());
            }
            if previous_count != 1 {
                return Err(io::Error::other(format!(
                    "unexpected primary-thread suspend count {previous_count}"
                )));
            }
            return Ok(());
        }

        entry.dwSize = size_of::<THREADENTRY32>() as u32;
        // SAFETY: `entry` is writable and the snapshot remains live.
        if unsafe { Thread32Next(snapshot.raw(), &mut entry) } == 0 {
            let error = unsafe { GetLastError() };
            if error == ERROR_NO_MORE_FILES {
                break;
            }
            return Err(io::Error::from_raw_os_error(error as i32));
        }
    }

    Err(io::Error::new(
        io::ErrorKind::NotFound,
        format!("could not find the suspended primary thread for process {pid}"),
    ))
}

pub(super) const CREATE_SUSPENDED_FLAG: u32 = CREATE_SUSPENDED;

#[cfg(test)]
#[path = "index_status_process_windows_tests.rs"]
mod tests;

#[cfg(test)]
pub(super) fn terminate_test_process(pid: u32) -> io::Result<()> {
    use windows_sys::Win32::System::Threading::{
        OpenProcess, PROCESS_SYNCHRONIZE, PROCESS_TERMINATE, TerminateProcess, WaitForSingleObject,
    };

    let process = unsafe { OpenProcess(PROCESS_TERMINATE | PROCESS_SYNCHRONIZE, 0, pid) };
    if process.is_null() {
        let error = unsafe { GetLastError() };
        return if error == windows_sys::Win32::Foundation::ERROR_INVALID_PARAMETER {
            Ok(())
        } else {
            Err(io::Error::from_raw_os_error(error as i32))
        };
    }
    let process = OwnedHandle::new(process)?;
    // SAFETY: `process` is an owned handle opened with terminate rights.
    if unsafe { TerminateProcess(process.raw(), 1) } == 0 {
        let error = io::Error::last_os_error();
        // It may have exited after OpenProcess and before termination.
        if unsafe { WaitForSingleObject(process.raw(), 0) }
            == windows_sys::Win32::Foundation::WAIT_OBJECT_0
        {
            return Ok(());
        }
        return Err(error);
    }
    // SAFETY: the handle is valid and has synchronize rights; the wait is
    // bounded so test cleanup cannot hang.
    match unsafe { WaitForSingleObject(process.raw(), 1_000) } {
        windows_sys::Win32::Foundation::WAIT_OBJECT_0 => Ok(()),
        windows_sys::Win32::Foundation::WAIT_TIMEOUT => Err(io::Error::new(
            io::ErrorKind::TimedOut,
            "Windows test descendant did not exit after termination",
        )),
        _ => Err(io::Error::last_os_error()),
    }
}

#[cfg(test)]
pub(super) fn process_is_gone(pid: u32, timeout: Duration) -> io::Result<bool> {
    use windows_sys::Win32::System::Threading::{OpenProcess, PROCESS_SYNCHRONIZE};

    let process = unsafe { OpenProcess(PROCESS_SYNCHRONIZE, 0, pid) };
    if process.is_null() {
        let error = unsafe { GetLastError() };
        return if error == windows_sys::Win32::Foundation::ERROR_INVALID_PARAMETER {
            Ok(true)
        } else {
            Err(io::Error::from_raw_os_error(error as i32))
        };
    }
    let process = OwnedHandle::new(process)?;
    let wait_ms = timeout.as_millis().min(u32::MAX as u128) as u32;
    // SAFETY: `process` is a live handle opened with PROCESS_SYNCHRONIZE.
    match unsafe {
        windows_sys::Win32::System::Threading::WaitForSingleObject(process.raw(), wait_ms)
    } {
        windows_sys::Win32::Foundation::WAIT_OBJECT_0 => Ok(true),
        windows_sys::Win32::Foundation::WAIT_TIMEOUT => Ok(false),
        _ => Err(io::Error::last_os_error()),
    }
}
