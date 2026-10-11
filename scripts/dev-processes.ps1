# Windows Job Object ownership for one dev launcher invocation.
if (-not ('KapsoraDevJob' -as [type])) {
    Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class KapsoraDevJob {
    [StructLayout(LayoutKind.Sequential)]
    private struct IO_COUNTERS {
        public ulong ReadOperationCount, WriteOperationCount, OtherOperationCount;
        public ulong ReadTransferCount, WriteTransferCount, OtherTransferCount;
    }
    [StructLayout(LayoutKind.Sequential)]
    private struct BASIC_LIMIT {
        public long PerProcessUserTimeLimit, PerJobUserTimeLimit;
        public uint LimitFlags;
        public UIntPtr MinimumWorkingSetSize, MaximumWorkingSetSize;
        public uint ActiveProcessLimit;
        public UIntPtr Affinity;
        public uint PriorityClass, SchedulingClass;
    }
    [StructLayout(LayoutKind.Sequential)]
    private struct EXTENDED_LIMIT {
        public BASIC_LIMIT BasicLimitInformation;
        public IO_COUNTERS IoInfo;
        public UIntPtr ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed;
    }
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateJobObject(IntPtr securityAttributes, string name);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr OpenJobObject(uint access, bool inherit, string name);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetInformationJobObject(IntPtr job, int infoClass, ref EXTENDED_LIMIT info, uint length);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool TerminateJobObject(IntPtr job, uint exitCode);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);
    public static IntPtr Create(string name) {
        IntPtr job = CreateJobObject(IntPtr.Zero, name);
        if (job == IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        if (Marshal.GetLastWin32Error() == 183) { CloseHandle(job); throw new InvalidOperationException("Dev job name already exists."); }
        EXTENDED_LIMIT limits = new EXTENDED_LIMIT();
        limits.BasicLimitInformation.LimitFlags = 0x2000;
        if (!SetInformationJobObject(job, 9, ref limits, (uint)Marshal.SizeOf(typeof(EXTENDED_LIMIT)))) {
            int error = Marshal.GetLastWin32Error();
            CloseHandle(job);
            throw new System.ComponentModel.Win32Exception(error);
        }
        return job;
    }
    public static void Assign(IntPtr job, int pid, long created) {
        using (var process = System.Diagnostics.Process.GetProcessById(pid)) {
            IntPtr processHandle = process.Handle;
            if (process.StartTime.ToUniversalTime().Ticks != created) throw new InvalidOperationException("Dev worker process identity changed.");
            if (!AssignProcessToJobObject(job, processHandle))
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        }
    }
    public static bool Terminate(IntPtr job) {
        if (job == IntPtr.Zero) return false;
        if (!TerminateJobObject(job, 1))
            throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        return true;
    }
    public static IntPtr Open(string name) {
        IntPtr job = OpenJobObject(0x0008, false, name);
        if (job == IntPtr.Zero && Marshal.GetLastWin32Error() != 2) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        return job;
    }
    public static void Close(IntPtr job) { if (job != IntPtr.Zero) CloseHandle(job); }
}
"@
}
function Get-DevProcessIdentity([int]$processId) {
    $process = Get-Process -Id $processId -ErrorAction SilentlyContinue
    if (-not $process) { return $null }
    [pscustomobject]@{ Id = $processId; Created = $process.StartTime.ToUniversalTime().Ticks }
}
function Test-DevProcessIdentity($identity) {
    if (-not $identity) { return $false }
    $current = Get-DevProcessIdentity ([int]$identity.Id)
    return ($null -ne $current -and [long]$current.Created -eq [long]$identity.Created)
}
