Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Diagnostics;
using System.Security.Principal;
using System.Text;
using System.Collections.Generic;
public static class HarborUIProfile {
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr OpenProcess(uint access, bool inherit, int pid);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetTokenInformation(IntPtr token, int kind, IntPtr data, int length, out int needed);
    [DllImport("advapi32.dll")] static extern IntPtr GetSidSubAuthorityCount(IntPtr sid);
    [DllImport("advapi32.dll")] static extern IntPtr GetSidSubAuthority(IntPtr sid, uint index);
    [StructLayout(LayoutKind.Sequential)] struct SidAndAttributes { public IntPtr Sid; public uint Attributes; }
    [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Unicode)] struct StartupInfo {
        public int cb; public string reserved; public string desktop; public string title;
        public uint x, y, width, height, charsX, charsY, fill, flags;
        public ushort show, reservedSize; public IntPtr reservedData, input, output, error;
    }
    [StructLayout(LayoutKind.Sequential)] struct ProcessInfo { public IntPtr process, thread; public uint pid, tid; }
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool CreateRestrictedToken(IntPtr existing, uint flags, uint disableCount, SidAndAttributes[] disable, uint deleteCount, IntPtr delete, uint restrictedCount, IntPtr restricted, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool ConvertStringSidToSid(string text, out IntPtr sid);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool SetTokenInformation(IntPtr token, int kind, IntPtr data, int length);
    [DllImport("advapi32.dll")] static extern uint GetLengthSid(IntPtr sid);
    [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr memory);
    [DllImport("advapi32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool CreateProcessAsUser(IntPtr token, string application, StringBuilder command, IntPtr processSecurity, IntPtr threadSecurity, bool inherit, uint flags, IntPtr environment, string directory, ref StartupInfo startup, out ProcessInfo process);
    static int TokenInteger(IntPtr token, int kind) {
        IntPtr data = Marshal.AllocHGlobal(4);
        try { int needed; if (!GetTokenInformation(token, kind, data, 4, out needed)) throw new System.ComponentModel.Win32Exception(); return Marshal.ReadInt32(data); }
        finally { Marshal.FreeHGlobal(data); }
    }
    public static void AssertProductProcess(int pid) {
        IntPtr process = OpenProcess(0x1000, false, pid);
        if (process == IntPtr.Zero) throw new System.ComponentModel.Win32Exception();
        IntPtr token = IntPtr.Zero;
        try {
            if (!OpenProcessToken(process, 10, out token)) throw new System.ComponentModel.Win32Exception();
            using (WindowsIdentity identity = new WindowsIdentity(token)) {
                if (identity.User.Value != WindowsIdentity.GetCurrent().User.Value) throw new Exception("product changed user SID");
                if (new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator)) throw new Exception("product retains administrator membership");
            }
            if (TokenInteger(token, 20) != 0 || Integrity(pid) != 8192) throw new Exception("product must have a non-elevated medium-integrity token");
            if (TokenInteger(token, 12) != Process.GetCurrentProcess().SessionId) throw new Exception("product changed interactive session");
        } finally { if (token != IntPtr.Zero) CloseHandle(token); CloseHandle(process); }
    }
    static readonly List<IntPtr> launches = new List<IntPtr>();
    [DllImport("kernel32.dll", SetLastError=true)] static extern uint ResumeThread(IntPtr thread);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetExitCodeProcess(IntPtr process, out uint code);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool TerminateProcess(IntPtr process, uint code);
    [DllImport("kernel32.dll", SetLastError=true)] static extern uint WaitForSingleObject(IntPtr process, uint milliseconds);
    public static void QuiesceLaunchedUIs() {
        foreach (IntPtr process in launches) {
            uint code;
            if (!GetExitCodeProcess(process, out code)) throw new System.ComponentModel.Win32Exception();
            if (code == 259 && !TerminateProcess(process, 1)) throw new System.ComponentModel.Win32Exception();
            if (WaitForSingleObject(process, 10000) != 0) throw new Exception("launched UI did not exit within the cleanup bound");
        }
    }
    public static void CloseLaunchReceipts() {
        foreach (IntPtr process in launches) CloseHandle(process);
        launches.Clear();
    }
    public static int Launch(string application, string directory) {
        IntPtr original = IntPtr.Zero, restricted = IntPtr.Zero, administrators = IntPtr.Zero, medium = IntPtr.Zero, label = IntPtr.Zero;
        ProcessInfo created = new ProcessInfo();
        bool recorded = false;
        try {
            if (!OpenProcessToken(Process.GetCurrentProcess().Handle, 0xF01FF, out original)) throw new System.ComponentModel.Win32Exception();
            if (!ConvertStringSidToSid("S-1-5-32-544", out administrators)) throw new System.ComponentModel.Win32Exception();
            SidAndAttributes[] disable = { new SidAndAttributes { Sid=administrators, Attributes=0 } };
            if (!CreateRestrictedToken(original, 1, 1, disable, 0, IntPtr.Zero, 0, IntPtr.Zero, out restricted)) throw new System.ComponentModel.Win32Exception();
            if (!ConvertStringSidToSid("S-1-16-8192", out medium)) throw new System.ComponentModel.Win32Exception();
            int length = Marshal.SizeOf(typeof(SidAndAttributes)) + (int)GetLengthSid(medium);
            label = Marshal.AllocHGlobal(length);
            Marshal.StructureToPtr(new SidAndAttributes { Sid=medium, Attributes=0x20 }, label, false);
            if (!SetTokenInformation(restricted, 25, label, length)) throw new System.ComponentModel.Win32Exception();
            StartupInfo startup = new StartupInfo(); startup.cb=Marshal.SizeOf(typeof(StartupInfo)); startup.desktop="winsta0\\default";
            if (application.Contains("\"")) throw new Exception("invalid executable path");
            if (!CreateProcessAsUser(restricted, application, new StringBuilder("\""+application+"\""), IntPtr.Zero, IntPtr.Zero, false, 4, IntPtr.Zero, directory, ref startup, out created)) throw new System.ComponentModel.Win32Exception();
            launches.Add(created.process);
            recorded = true;
            AssertProductProcess((int)created.pid);
            if (ResumeThread(created.thread) == uint.MaxValue) throw new System.ComponentModel.Win32Exception();
            return (int)created.pid;
        } finally {
            if (created.thread != IntPtr.Zero) CloseHandle(created.thread);
            if (created.process != IntPtr.Zero && !recorded) {
                TerminateProcess(created.process, 1);
                WaitForSingleObject(created.process, 10000);
                CloseHandle(created.process);
            }
            if (label != IntPtr.Zero) Marshal.FreeHGlobal(label);
            if (medium != IntPtr.Zero) LocalFree(medium);
            if (administrators != IntPtr.Zero) LocalFree(administrators);
            if (restricted != IntPtr.Zero) CloseHandle(restricted);
            if (original != IntPtr.Zero) CloseHandle(original);
        }
    }
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
    # The hosted Explorer shell may be elevated. Product launch uses a restricted token instead.
    if ($session -ne [Diagnostics.Process]::GetCurrentProcess().SessionId) { throw 'interactive session drifted' }
}
