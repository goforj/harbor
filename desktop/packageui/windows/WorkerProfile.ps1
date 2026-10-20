Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class HarborUIProfile {
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr OpenProcess(uint access, bool inherit, int pid);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetTokenInformation(IntPtr token, int kind, IntPtr data, int length, out int needed);
    [DllImport("advapi32.dll")] static extern IntPtr GetSidSubAuthorityCount(IntPtr sid);
    [DllImport("advapi32.dll")] static extern IntPtr GetSidSubAuthority(IntPtr sid, uint index);
    public static int Integrity(int pid) {
        IntPtr process = OpenProcess(0x1000, false, pid);
        if (process == IntPtr.Zero) throw new System.ComponentModel.Win32Exception();
        IntPtr token = IntPtr.Zero;
        IntPtr data = IntPtr.Zero;
        try {
            if (!OpenProcessToken(process, 8, out token)) throw new System.ComponentModel.Win32Exception();
            int needed;
            GetTokenInformation(token, 25, IntPtr.Zero, 0, out needed);
            if (needed <= 0) throw new Exception("token integrity size unavailable");
            data = Marshal.AllocHGlobal(needed);
            if (!GetTokenInformation(token, 25, data, needed, out needed)) throw new System.ComponentModel.Win32Exception();
            IntPtr sid = Marshal.ReadIntPtr(data);
            byte count = Marshal.ReadByte(GetSidSubAuthorityCount(sid));
            if (count == 0) throw new Exception("token integrity SID is incomplete");
            return Marshal.ReadInt32(GetSidSubAuthority(sid, (uint)(count - 1)));
        } finally {
            if (data != IntPtr.Zero) Marshal.FreeHGlobal(data);
            if (token != IntPtr.Zero) CloseHandle(token);
            CloseHandle(process);
        }
    }
}
'@
function Assert-InteractiveWorker {
    $session = (Get-Process -Id $PID).SessionId
    if ($session -le 0 -or -not [Environment]::UserInteractive) { throw 'installed GUI acceptance requires a real interactive Windows session' }
    $shells = @(Get-Process -Name explorer -ErrorAction SilentlyContinue | Where-Object { $_.SessionId -eq $session })
    if ($shells.Count -ne 1 -or [HarborUIProfile]::Integrity($shells[0].Id) -ne 8192) {
        throw 'installed GUI acceptance requires one interactive medium-integrity Explorer shell'
    }
    if ((Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System').EnableLUA -ne 1) {
        throw 'installed GUI acceptance requires UAC enabled and a filtered administrator token'
    }
}
