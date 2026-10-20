using System;
using System.Runtime.InteropServices;
using System.Diagnostics;
using System.Security.Principal;
using System.Text;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.IO.Pipes;
using System.Security.AccessControl;
public static class HarborUIProfile {
    [DllImport("kernel32.dll")] static extern IntPtr GetCurrentProcess();
    [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr OpenProcess(uint access, bool inherit, int pid);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    [DllImport("kernel32.dll", SetLastError=true)] static extern uint GetProcessId(IntPtr process);
    [DllImport("user32.dll")] static extern bool IsWindow(IntPtr window);
    [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr window);
    [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] static extern bool SetForegroundWindow(IntPtr window);
    [DllImport("user32.dll")] static extern IntPtr GetMenu(IntPtr window);
    [DllImport("user32.dll")] static extern IntPtr GetSubMenu(IntPtr menu, int position);
    [DllImport("user32.dll")] static extern int GetMenuItemCount(IntPtr menu);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetMenuString(IntPtr menu, uint item, StringBuilder text, int maximum, uint flags);
    [DllImport("user32.dll", SetLastError=true)] static extern uint GetWindowThreadProcessId(IntPtr window, out uint pid);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool GetTokenInformation(IntPtr token, int kind, IntPtr data, int length, out int needed);
    [DllImport("advapi32.dll")] static extern IntPtr GetSidSubAuthorityCount(IntPtr sid);
    [DllImport("advapi32.dll")] static extern IntPtr GetSidSubAuthority(IntPtr sid, uint index);
    [StructLayout(LayoutKind.Sequential)] struct SidAndAttributes { public IntPtr Sid; public uint Attributes; }
    [StructLayout(LayoutKind.Sequential)] struct TokenGroups { public uint count; public SidAndAttributes first; }
    [StructLayout(LayoutKind.Sequential)] struct Luid { public uint low; public int high; }
    [StructLayout(LayoutKind.Sequential)] struct LuidAndAttributes { public Luid luid; public uint attributes; }
    [StructLayout(LayoutKind.Sequential)] struct TokenPrivileges { public uint count; public LuidAndAttributes first; }
    [StructLayout(LayoutKind.Sequential, CharSet=CharSet.Unicode)] struct StartupInfo {
        public int cb; public string reserved; public string desktop; public string title;
        public uint x, y, width, height, charsX, charsY, fill, flags;
        public ushort show, reservedSize; public IntPtr reservedData, input, output, error;
    }
    [StructLayout(LayoutKind.Sequential)] struct ProcessInfo { public IntPtr process, thread; public uint pid, tid; }
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool CreateRestrictedToken(IntPtr existing, uint flags, uint disableCount, SidAndAttributes[] disable, uint deleteCount, LuidAndAttributes[] delete, uint restrictedCount, IntPtr restricted, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool LookupPrivilegeName(string system, ref Luid luid, StringBuilder name, ref int length);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool DuplicateTokenEx(IntPtr source, uint access, IntPtr security, int level, int kind, out IntPtr duplicate);
    [DllImport("kernel32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern IntPtr CreateFile(string path, uint access, uint share, IntPtr security, uint disposition, uint flags, IntPtr template);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode)] static extern int RegOpenKeyEx(IntPtr root, string name, uint options, uint access, out IntPtr key);
    [DllImport("advapi32.dll")] static extern int RegCloseKey(IntPtr key);
    [DllImport("advapi32.dll", SetLastError=true)] static extern uint GetSecurityInfo(IntPtr handle, int kind, uint information, out IntPtr owner, out IntPtr group, out IntPtr dacl, out IntPtr sacl, out IntPtr descriptor);
    [DllImport("advapi32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool ConvertSecurityDescriptorToStringSecurityDescriptor(IntPtr descriptor, uint revision, uint information, out IntPtr text, out uint length);
    [DllImport("advapi32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool ConvertStringSidToSid(string text, out IntPtr sid);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool SetTokenInformation(IntPtr token, int kind, IntPtr data, int length);
    [DllImport("advapi32.dll")] static extern uint GetLengthSid(IntPtr sid);
    [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr memory);
    [DllImport("advapi32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool CreateProcessAsUser(IntPtr token, string application, StringBuilder command, IntPtr processSecurity, IntPtr threadSecurity, bool inherit, uint flags, IntPtr environment, string directory, ref StartupInfo startup, out ProcessInfo process);
    [DllImport("kernel32.dll", SetLastError=true, CharSet=CharSet.Unicode)] static extern bool CreateProcess(string application, StringBuilder command, IntPtr processSecurity, IntPtr threadSecurity, bool inherit, uint flags, IntPtr environment, string directory, ref StartupInfo startup, out ProcessInfo process);
    static int TokenInteger(IntPtr token, int kind) {
        IntPtr data = Marshal.AllocHGlobal(4);
        try { int needed; if (!GetTokenInformation(token, kind, data, 4, out needed)) throw new System.ComponentModel.Win32Exception(); return Marshal.ReadInt32(data); }
        finally { Marshal.FreeHGlobal(data); }
    }
    public static string LastLaunchFacts = "not launched";
    public static string[] ProtectedDirectories = new string[0];
    public static string[] ProtectedFiles = new string[0];
    public static string ProtectedRegistryKey = "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Policies\\System";
    public static string ProcessProbeScript;
    public static readonly List<string> AccessReceipts = new List<string>();
    static IntPtr TokenBuffer(IntPtr token, int kind) {
        int length;
        if (GetTokenInformation(token, kind, IntPtr.Zero, 0, out length) || Marshal.GetLastWin32Error() != 122 || length <= 0 || length > 1048576) throw new Exception("invalid token information size");
        IntPtr data = Marshal.AllocHGlobal(length);
        if (!GetTokenInformation(token, kind, data, length, out length)) {
            int error = Marshal.GetLastWin32Error(); Marshal.FreeHGlobal(data); throw new Win32Exception(error);
        }
        return data;
    }
    static LuidAndAttributes[] Privileges(IntPtr token) {
        IntPtr data = TokenBuffer(token, 3);
        try {
            int count = Marshal.ReadInt32(data);
            if (count < 0 || count > 1024) throw new Exception("invalid token privilege count");
            LuidAndAttributes[] values = new LuidAndAttributes[count];
            int offset = (int)Marshal.OffsetOf(typeof(TokenPrivileges), "first");
            for (int index=0; index<count; index++) values[index] = (LuidAndAttributes)Marshal.PtrToStructure(IntPtr.Add(data, offset+index*Marshal.SizeOf(typeof(LuidAndAttributes))), typeof(LuidAndAttributes));
            return values;
        } finally { Marshal.FreeHGlobal(data); }
    }
    static string PrivilegeName(Luid luid) {
        int length=0;
        LookupPrivilegeName(null, ref luid, null, ref length);
        if (length <= 0 || length > 1024) throw new Exception("invalid privilege name size");
        StringBuilder name = new StringBuilder(length+1);
        if (!LookupPrivilegeName(null, ref luid, name, ref length)) throw new Win32Exception();
        return name.ToString();
    }
    static int TokenIntegrity(IntPtr token) {
        IntPtr data = TokenBuffer(token, 25);
        try {
            IntPtr sid = Marshal.ReadIntPtr(data);
            byte count = Marshal.ReadByte(GetSidSubAuthorityCount(sid));
            if (count == 0) throw new Exception("token integrity SID is incomplete");
            return Marshal.ReadInt32(GetSidSubAuthority(sid, (uint)(count-1)));
        } finally { Marshal.FreeHGlobal(data); }
    }
    static byte[] DefaultDaclBytes(IntPtr token) {
        IntPtr information=TokenBuffer(token, 6);
        try {
            IntPtr acl=Marshal.ReadIntPtr(information);
            if (acl == IntPtr.Zero) throw new Exception("token default DACL is null");
            int length=(ushort)Marshal.ReadInt16(acl, 2);
            if (length < 8) throw new Exception("token default DACL has an invalid size");
            byte[] bytes=new byte[length];
            Marshal.Copy(acl, bytes, 0, length);
            return bytes;
        } finally { Marshal.FreeHGlobal(information); }
    }
    static void AssertPrivateDefaultDacl(IntPtr token) {
        RawAcl acl=new RawAcl(DefaultDaclBytes(token), 0);
        string user;
        using (WindowsIdentity identity=new WindowsIdentity(token)) { user=identity.User.Value; }
        if (acl.Count != 2) throw new Exception("token default DACL must contain exactly user and SYSTEM");
        HashSet<string> seen=new HashSet<string>();
        foreach (GenericAce entry in acl) {
            CommonAce ace=entry as CommonAce;
            if (ace == null || ace.AceQualifier != AceQualifier.AccessAllowed || ace.AceFlags != AceFlags.None || ace.AccessMask != 0x10000000) throw new Exception("token default DACL has unexpected rights or inheritance");
            string sid=ace.SecurityIdentifier.Value;
            if ((sid != user && sid != "S-1-5-18") || !seen.Add(sid)) throw new Exception("token default DACL has a foreign or duplicate principal");
        }
    }
    static void ConfigurePrivateDefaultDacl(IntPtr token) {
        string user;
        using (WindowsIdentity identity=new WindowsIdentity(token)) { user=identity.User.Value; }
        RawAcl acl=new RawSecurityDescriptor("D:(A;;GA;;;"+user+")(A;;GA;;;SY)").DiscretionaryAcl;
        byte[] bytes=new byte[acl.BinaryLength];
        acl.GetBinaryForm(bytes, 0);
        IntPtr nativeAcl=Marshal.AllocHGlobal(bytes.Length);
        IntPtr information=Marshal.AllocHGlobal(IntPtr.Size);
        try {
            Marshal.Copy(bytes, 0, nativeAcl, bytes.Length);
            Marshal.WriteIntPtr(information, nativeAcl);
            if (!SetTokenInformation(token, 6, information, IntPtr.Size)) throw new Win32Exception();
        } finally { Marshal.FreeHGlobal(information); Marshal.FreeHGlobal(nativeAcl); }
        AssertPrivateDefaultDacl(token);
    }
    static void AssertEffectiveToken(IntPtr token) {
        AssertPrivateDefaultDacl(token);
        string userSID;
        using (WindowsIdentity identity = new WindowsIdentity(token)) {
            userSID=identity.User.Value;
            if (identity.User.Value != WindowsIdentity.GetCurrent().User.Value) throw new Exception("product changed user SID");
            if (new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator)) throw new Exception("product retains enabled administrator membership");
        }
        IntPtr owner = TokenBuffer(token, 4);
        try { if (new SecurityIdentifier(Marshal.ReadIntPtr(owner)).Value != userSID) throw new Exception("product default object owner differs from its user SID"); }
        finally { Marshal.FreeHGlobal(owner); }
        IntPtr groups = TokenBuffer(token, 2);
        try {
            int count = Marshal.ReadInt32(groups);
            if (count < 0 || count > 4096) throw new Exception("invalid token group count");
            int offset = (int)Marshal.OffsetOf(typeof(TokenGroups), "first");
            for (int index=0; index<count; index++) {
                SidAndAttributes group = (SidAndAttributes)Marshal.PtrToStructure(IntPtr.Add(groups, offset+index*Marshal.SizeOf(typeof(SidAndAttributes))), typeof(SidAndAttributes));
                if (new SecurityIdentifier(group.Sid).Value == "S-1-5-32-544" && ((group.Attributes & 0x10) == 0 || (group.Attributes & 0x4) != 0)) throw new Exception("administrator SID is not exclusively deny-only");
            }
        } finally { Marshal.FreeHGlobal(groups); }
        foreach (LuidAndAttributes privilege in Privileges(token)) {
            if (PrivilegeName(privilege.luid) != "SeChangeNotifyPrivilege") throw new Exception("product retains removable privilege "+PrivilegeName(privilege.luid));
        }
        if (TokenInteger(token, 20) != 0 || TokenIntegrity(token) != 8192 || (TokenInteger(token, 27) & 1) == 0 || TokenInteger(token, 26) != 0) throw new Exception("product requires non-elevated medium integrity, no-write-up, and UIAccess disabled");
        if (TokenInteger(token, 12) != Process.GetCurrentProcess().SessionId) throw new Exception("product changed interactive session");
    }
    static string LinkedTokenFacts(IntPtr token) {
        IntPtr data = Marshal.AllocHGlobal(IntPtr.Size);
        IntPtr linked = IntPtr.Zero;
        try {
            int needed;
            if (!GetTokenInformation(token, 19, data, IntPtr.Size, out needed)) return "unavailable:"+Marshal.GetLastWin32Error();
            linked = Marshal.ReadIntPtr(data);
            if (linked == IntPtr.Zero) return "unavailable:null";
            return "available:elevation="+TokenInteger(linked, 20);
        } finally { if (linked != IntPtr.Zero) CloseHandle(linked); Marshal.FreeHGlobal(data); }
    }
    static string DescribeToken(IntPtr token, int pid) {
        using (WindowsIdentity identity = new WindowsIdentity(token)) {
            return "elevation="+TokenInteger(token, 20)+";integrity="+TokenIntegrity(token)+";administrator="+new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator)+";elevation_type="+TokenInteger(token, 18)+";linked_token="+LinkedTokenFacts(token)+";mandatory_policy="+TokenInteger(token, 27)+";ui_access="+TokenInteger(token, 26)+";privilege_count="+Privileges(token).Length;
        }
    }
    public static string CurrentTokenFacts() {
        IntPtr token;
        if (!OpenProcessToken(GetCurrentProcess(), 10, out token)) throw new System.ComponentModel.Win32Exception();
        try { return DescribeToken(token, Process.GetCurrentProcess().Id); }
        finally { CloseHandle(token); }
    }
    static string EffectiveControllerFacts() {
        using (WindowsIdentity identity=WindowsIdentity.GetCurrent()) {
            List<string> privileges=new List<string>();
            foreach (LuidAndAttributes privilege in Privileges(identity.Token)) privileges.Add(PrivilegeName(privilege.luid)+":"+privilege.attributes);
            return DescribeToken(identity.Token, Process.GetCurrentProcess().Id)+";privileges="+String.Join(",", privileges.ToArray());
        }
    }
    public static void AssertProductProcess(int pid) {
        IntPtr process = OpenProcess(0x1000, false, pid);
        if (process == IntPtr.Zero) throw new System.ComponentModel.Win32Exception();
        IntPtr token = IntPtr.Zero;
        try {
            if (!OpenProcessToken(process, 10, out token)) throw new System.ComponentModel.Win32Exception();
            AssertChildToken(token, pid, false);
        } finally { if (token != IntPtr.Zero) CloseHandle(token); CloseHandle(process); }
    }
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool QueryFullProcessImageName(IntPtr process, uint flags, StringBuilder path, ref uint size);
    public static bool HasRetainedProductExited(int pid) {
        bool found=false;
        foreach (IntPtr process in launches) {
            if (GetProcessId(process) != (uint)pid) continue;
            found=true;
            uint state=WaitForSingleObject(process, 0);
            if (state == 258) return false;
            if (state != 0) throw new Win32Exception(Marshal.GetLastWin32Error(), "observe exact retained product exit");
        }
        if (!found) throw new Exception("exit observation has no exact retained product process");
        return true;
    }
    // RetainedProductMenu records the attached native menu rather than assuming UIAutomation exposes nonclient controls.
    public static string[] RetainedProductMenu(int pid, IntPtr window) {
        if (!IsRetainedProductWindowVisible(pid, window)) throw new Exception("menu target is not the visible admitted product window");
        IntPtr menu=GetMenu(window);
        if (menu == IntPtr.Zero) throw new Exception("the admitted product window has no attached native menu");
        int count=GetMenuItemCount(menu);
        if (count < 1 || count > 32) throw new Exception("unexpected native application menu size");
        List<string> names=new List<string>();
        for (int i=0; i<count; i++) {
            StringBuilder name=new StringBuilder(1024);
            if (GetMenuString(menu, (uint)i, name, name.Capacity, 0x400) < 1) throw new Exception("native application menu label is unavailable");
            string label=name.ToString().Replace("&", "");
            names.Add(label);
            if (label != "Harbor") continue;
            IntPtr sub=GetSubMenu(menu, i);
            int items=GetMenuItemCount(sub);
            if (sub == IntPtr.Zero || items < 1 || items > 32) throw new Exception("native Harbor submenu is unavailable");
            for (int j=0; j<items; j++) {
                name.Length=0;
                if (GetMenuString(sub, (uint)j, name, name.Capacity, 0x400) > 0) names.Add("Harbor/" + name.ToString().Replace("&", ""));
            }
        }
        return names.ToArray();
    }
    public static bool IsRetainedProductWindowForeground(int pid, IntPtr window) {
        if (!IsRetainedProductWindowVisible(pid, window)) throw new Exception("foreground target is not the visible admitted product window");
        return GetForegroundWindow() == window;
    }
    public static void ActivateRetainedProductWindow(int pid, IntPtr window) {
        if (IsRetainedProductWindowForeground(pid, window)) return;
        SetForegroundWindow(window);
    }
    public static bool IsRetainedProductWindowVisible(int pid, IntPtr window) {
        bool live=false;
        foreach (IntPtr process in launches) {
            if (GetProcessId(process) == (uint)pid && WaitForSingleObject(process, 0) == 258) { live=true; break; }
        }
        if (!live) throw new Exception("window observation has no exact live retained product process");
        if (window == IntPtr.Zero || !IsWindow(window)) throw new Exception("the admitted product window was destroyed instead of hidden");
        uint owner;
        if (GetWindowThreadProcessId(window, out owner) == 0) throw new Win32Exception(Marshal.GetLastWin32Error(), "identify retained product window");
        if (owner != (uint)pid) throw new Exception("native product window belongs to another process");
        return IsWindowVisible(window);
    }
    public static void RetainProductProcess(int pid, string expectedImage) {
        IntPtr process = OpenProcess(0x101001, false, pid);
        if (process == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "retain actual installed process");
        IntPtr token = IntPtr.Zero;
        bool retained = false;
        try {
            StringBuilder image = new StringBuilder(32768);
            uint size = (uint)image.Capacity;
            if (!QueryFullProcessImageName(process, 0, image, ref size)) throw new Win32Exception(Marshal.GetLastWin32Error(), "query retained installed process image");
            if (!String.Equals(image.ToString(), expectedImage, StringComparison.OrdinalIgnoreCase)) throw new Exception("actual product process image differs from the selected installation");
            if (WaitForSingleObject(process, 0) != 258) throw new Exception("selected product process is no longer running");
            if (!OpenProcessToken(process, 10, out token)) throw new Win32Exception(Marshal.GetLastWin32Error(), "open actual installed primary token");
            AssertEffectiveToken(token);
            // Retain the admitted exact process before optional diagnostics can fail.
            launches.Add(process);
            retained = true;
            AssertChildToken(token, pid, false);
        } finally {
            if (token != IntPtr.Zero) CloseHandle(token);
            if (!retained) CloseHandle(process);
        }
    }
    // Product checks retain exact-token machine probes; only the standalone fixture gathers cross-high observations.
    static void AssertChildToken(IntPtr token, int pid, bool observeCrossHigh) {
        LastLaunchFacts = "profile=hosted-preauthorized-restricted-functional;"+DescribeToken(token, pid);
        AssertEffectiveToken(token);
        ProveEffectiveAccess(token, observeCrossHigh);
    }
    static void RequireDeniedFile(string path, uint right, bool directory) {
        IntPtr file = CreateFile(path, right, 7, IntPtr.Zero, 3, directory ? 0x02000000u : 0u, IntPtr.Zero);
        int error = Marshal.GetLastWin32Error();
        if (file != new IntPtr(-1)) { CloseHandle(file); throw new Exception("restricted token can open protected path for right "+right+": "+path); }
        if (error != 5) throw new Win32Exception(error, "protected path denial was not access denied: "+path);
        AccessReceipts.Add("file:denied:"+right+":"+path);
    }
    public static void ObserveNativeProcessRights(int highPID) {
        IntPtr token;
        if (!OpenProcessToken(GetCurrentProcess(), 10, out token)) throw new Win32Exception();
        try { AssertEffectiveToken(token); } finally { CloseHandle(token); }
        List<string> granted=new List<string>();
        foreach (uint right in new uint[] { 1, 2, 0x20, 0x40, 0x40000, 0x80000 }) {
            IntPtr process=OpenProcess(right, false, highPID);
            int error=Marshal.GetLastWin32Error();
            if (process != IntPtr.Zero) { CloseHandle(process); granted.Add("process:"+right); AccessReceipts.Add("observed-grant:process:"+right); }
            else if (error != 5) throw new Win32Exception(error, "high-process denial was not access denied");
            else AccessReceipts.Add("process:denied:"+right);
        }
        IntPtr highProcess=OpenProcess(0x1000, false, highPID);
        if (highProcess == IntPtr.Zero) throw new Win32Exception();
        try {
            foreach (uint right in new uint[] { 1, 2, 4 }) {
                IntPtr highToken;
                if (OpenProcessToken(highProcess, right, out highToken)) { CloseHandle(highToken); granted.Add("token:"+right); AccessReceipts.Add("observed-grant:token:"+right); continue; }
                int error=Marshal.GetLastWin32Error();
                if (error != 5) throw new Win32Exception(error, "high-token denial was not access denied");
                AccessReceipts.Add("token:denied:"+right);
            }
        } finally { CloseHandle(highProcess); }
        highProcess=OpenProcess(0x1000, false, highPID);
        if (highProcess == IntPtr.Zero) throw new Win32Exception();
        try {
            IntPtr highToken;
            if (OpenProcessToken(highProcess, 10, out highToken)) {
                try { AccessReceipts.Add("opened-high-token:"+DescribeToken(highToken, highPID)); }
                finally { CloseHandle(highToken); }
            } else AccessReceipts.Add("opened-high-token:query-duplicate-denied:"+Marshal.GetLastWin32Error());
        } finally { CloseHandle(highProcess); }
        AccessReceipts.Add("containment:unsupported;observed-grants="+String.Join(",", granted.ToArray()));
    }
    static string ObjectSecurity(IntPtr handle) {
        IntPtr owner, group, dacl, sacl, descriptor, text=IntPtr.Zero;
        uint error=GetSecurityInfo(handle, 6, 0x15, out owner, out group, out dacl, out sacl, out descriptor);
        if (error != 0) throw new Win32Exception((int)error, "read retained high process security");
        try {
            uint length;
            if (!ConvertSecurityDescriptorToStringSecurityDescriptor(descriptor, 1, 0x15, out text, out length)) throw new Win32Exception();
            return Marshal.PtrToStringUni(text);
        } finally { if (text != IntPtr.Zero) LocalFree(text); LocalFree(descriptor); }
    }
    static void RunNativeProcessProbe(IntPtr token, int highPID, string privateRoot) {
        string result=Path.Combine(privateRoot, "process-probe.json");
        if (String.IsNullOrEmpty(ProcessProbeScript) || ProcessProbeScript.Contains("\"") || result.Contains("\"")) throw new Exception("native process probe path is invalid");
        string executable=Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), "WindowsPowerShell", "v1.0", "powershell.exe");
        StringBuilder command=new StringBuilder("\""+executable+"\" -NoProfile -NonInteractive -File \""+ProcessProbeScript+"\" -HighProcessId "+highPID+" -ResultPath \""+result+"\"");
        StartupInfo startup=new StartupInfo(); startup.cb=Marshal.SizeOf(typeof(StartupInfo)); startup.desktop="winsta0\\default";
        ProcessInfo probe=new ProcessInfo();
        Exception originalFailure=null;
        try {
            AccessReceipts.Add("probe-controller:"+EffectiveControllerFacts());
            if (!CreateProcessAsUser(token, executable, command, IntPtr.Zero, IntPtr.Zero, false, 4, IntPtr.Zero, privateRoot, ref startup, out probe)) throw new Win32Exception(Marshal.GetLastWin32Error(), "launch standalone permission observer with its directly derived fixture token");
            IntPtr actual;
            if (!OpenProcessToken(probe.process, 10, out actual)) throw new Win32Exception(Marshal.GetLastWin32Error(), "inspect actual permission-probe child token");
            try { AssertEffectiveToken(actual); } finally { CloseHandle(actual); }
            if (ResumeThread(probe.thread) == uint.MaxValue) throw new Win32Exception(Marshal.GetLastWin32Error(), "resume admitted permission-probe child");
            if (WaitForSingleObject(probe.process, 30000) != 0) throw new Exception("native permission probe exceeded its bound");
            uint code;
            if (!GetExitCodeProcess(probe.process, out code)) throw new Win32Exception();
            string receipt=File.Exists(result) ? File.ReadAllText(result) : "missing native permission receipt";
            AccessReceipts.Add("actual-process-probe:"+receipt);
            if (code != 0) throw new Exception("actual restricted process denied-permission probe failed: "+receipt);
        } catch (Exception error) { originalFailure=error; throw; }
        finally {
            FinishCleanup(originalFailure, new Action[] {
                delegate { RetireOwnedProbe(ref probe.process, "native-permission-probe"); },
                delegate { if (probe.thread != IntPtr.Zero) CloseHandle(probe.thread); }
            });
        }
    }
    static void RetireOwnedProbe(ref IntPtr process, string role) {
        if (process == IntPtr.Zero) return;
        IntPtr retained=process;
        process=IntPtr.Zero;
        try {
            uint code;
            if (!GetExitCodeProcess(retained, out code)) throw new Win32Exception();
            if (code == 259 && !TerminateProcess(retained, 1)) throw new Win32Exception();
            if (WaitForSingleObject(retained, 10000) != 0) throw new Exception(role+" did not retire within the cleanup bound");
        } catch {
            // Keep the exact native handle for the outer recovery attempt and its partial receipt.
            launches.Add(retained);
            throw;
        }
        CloseHandle(retained);
        AccessReceipts.Add(role+":quiescent");
    }
    static void FinishCleanup(Exception original, Action[] actions) {
        List<Exception> errors=new List<Exception>();
        foreach (Action action in actions) {
            try { action(); } catch (Exception error) { errors.Add(error); }
        }
        if (errors.Count != 0) {
            if (original != null) errors.Insert(0, original);
            throw new AggregateException("native permission fixture cleanup failed", errors);
        }
    }
    static void ProveEffectiveAccess(IntPtr token, bool observeCrossHigh) {
        IntPtr ownToken=IntPtr.Zero, impersonation=IntPtr.Zero;
        string privateRoot=null;
        NamedPipeServerStream server=null;
        IAsyncResult connection=null;
        ProcessInfo highFixture=new ProcessInfo();
        Exception originalFailure=null;
        try {
            if (!OpenProcessToken(GetCurrentProcess(), 8, out ownToken)) throw new Win32Exception();
            if (TokenIntegrity(ownToken) <= 8192) throw new Exception("hosted fixture requires an independently high parent for direct authority probes");
            string sid=WindowsIdentity.GetCurrent().User.Value;
            if (observeCrossHigh) {
                string systemRoot=Environment.GetFolderPath(Environment.SpecialFolder.System);
                string systemProbe=Path.Combine(systemRoot, "whoami.exe");
                StartupInfo startup=new StartupInfo(); startup.cb=Marshal.SizeOf(typeof(StartupInfo));
                if (!CreateProcess(systemProbe, new StringBuilder("\""+systemProbe+"\""), IntPtr.Zero, IntPtr.Zero, false, 4, IntPtr.Zero, systemRoot, ref startup, out highFixture)) throw new Win32Exception();
                IntPtr highFixtureToken;
                if (!OpenProcessToken(highFixture.process, 0x2000A, out highFixtureToken)) throw new Win32Exception();
                try {
                    using (WindowsIdentity identity=new WindowsIdentity(highFixtureToken)) {
                        if (identity.User.Value != sid || !new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator) || TokenIntegrity(highFixtureToken) <= 8192 || TokenInteger(highFixtureToken, 12) != Process.GetCurrentProcess().SessionId) throw new Exception("high fixture does not have ordinary same-user interactive administrator authority");
                    }
                    AccessReceipts.Add("high-fixture:token-object-security:"+ObjectSecurity(highFixtureToken));
                } finally { CloseHandle(highFixtureToken); }
                AccessReceipts.Add("high-fixture:object-security:"+ObjectSecurity(highFixture.process));
            }
            privateRoot=Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "harbor-ui-token-"+Guid.NewGuid().ToString("N"));
            DirectorySecurity security=new DirectorySecurity();
            security.SetSecurityDescriptorSddlForm("O:"+sid+"D:P(A;OICI;FA;;;"+sid+")");
            PipeSecurity pipeSecurity=new PipeSecurity();
            pipeSecurity.SetSecurityDescriptorSddlForm("O:"+sid+"D:P(A;;GA;;;"+sid+")");
            string pipeName="harbor-ui-token-"+Guid.NewGuid().ToString("N");
            if (!DuplicateTokenEx(token, 0xC, IntPtr.Zero, 2, 2, out impersonation)) throw new Win32Exception(Marshal.GetLastWin32Error(), "derive exact-token impersonation probe");
            using (WindowsImpersonationContext context=WindowsIdentity.Impersonate(impersonation)) {
                foreach (string path in ProtectedDirectories) foreach (uint right in new uint[] { 2, 4, 0x40, 0x10000, 0x40000, 0x80000 }) RequireDeniedFile(path, right, true);
                foreach (string path in ProtectedFiles) foreach (uint right in new uint[] { 2, 4, 0x10000, 0x40000, 0x80000 }) RequireDeniedFile(path, right, false);
                foreach (uint right in new uint[] { 2, 4, 0x10000, 0x40000, 0x80000 }) {
                    IntPtr key;
                    int error=RegOpenKeyEx(new IntPtr(unchecked((int)0x80000002)), ProtectedRegistryKey, 0, right, out key);
                    if (error == 0) { RegCloseKey(key); throw new Exception("restricted token retains protected HKLM right "+right); }
                    if (error != 5) throw new Win32Exception(error, "protected registry denial was not access denied");
                    AccessReceipts.Add("registry:denied:"+right);
                }
                // Positive objects originate under the measured child token, without privileged SACL authoring.
                Directory.CreateDirectory(privateRoot, security);
                string privateFile=Path.Combine(privateRoot, "positive.txt");
                File.WriteAllText(privateFile, "actual child token can write private user data");
                if (File.ReadAllText(privateFile) != "actual child token can write private user data") throw new Exception("private data readback differs");
                if (File.GetAccessControl(privateFile).GetOwner(typeof(SecurityIdentifier)).Value != sid) throw new Exception("child-created private file has a different owner");
                AccessReceipts.Add("private-user-file:read-write");
                server=new NamedPipeServerStream(pipeName, PipeDirection.InOut, 1, PipeTransmissionMode.Byte, PipeOptions.Asynchronous, 4096, 4096, pipeSecurity);
                connection=server.BeginWaitForConnection(null, null);
                IntPtr client=CreateFile("\\\\.\\pipe\\"+pipeName, 0xC0000000, 0, IntPtr.Zero, 3, 0, IntPtr.Zero);
                if (client == new IntPtr(-1)) throw new Win32Exception();
                CloseHandle(client);
                AccessReceipts.Add("same-user-pipe:read-write-open");
            }
            if (!connection.AsyncWaitHandle.WaitOne(5000)) throw new Exception("private IPC connection did not complete");
            server.EndWaitForConnection(connection);
            AccessReceipts.Add("machine-access:actual-token-impersonation;private-file-and-ipc:passed");
            if (observeCrossHigh) RunNativeProcessProbe(token, (int)highFixture.pid, privateRoot);
            else AccessReceipts.Add("actual-product-cross-high-observer:skipped;primary-token-facts:measured;machine-access:actual-token-impersonation;containment:unsupported");
        } catch (Exception error) { originalFailure=error; throw; }
        finally {
            FinishCleanup(originalFailure, new Action[] {
                delegate { RetireOwnedProbe(ref highFixture.process, "high-fixture:never-resumed"); },
                delegate { if (highFixture.thread != IntPtr.Zero) CloseHandle(highFixture.thread); },
                delegate { if (impersonation != IntPtr.Zero) CloseHandle(impersonation); },
                delegate { if (ownToken != IntPtr.Zero) CloseHandle(ownToken); },
                delegate { if (server != null) server.Dispose(); },
                delegate { if (privateRoot != null && Directory.Exists(privateRoot)) Directory.Delete(privateRoot, true); }
            });
        }
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
            if (ProtectedDirectories.Length == 0) ProtectedDirectories = new string[] { directory, Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "GoForj", "Harbor", "Privileged") };
            if (ProtectedFiles.Length == 0) ProtectedFiles = new string[] { application };
            if (!OpenProcessToken(GetCurrentProcess(), 0xF01FF, out original)) throw new System.ComponentModel.Win32Exception();
            if (!ConvertStringSidToSid("S-1-5-32-544", out administrators)) throw new System.ComponentModel.Win32Exception();
            SidAndAttributes[] disable = { new SidAndAttributes { Sid=administrators, Attributes=0 } };
            List<LuidAndAttributes> deleted=new List<LuidAndAttributes>();
            foreach (LuidAndAttributes privilege in Privileges(original)) if (PrivilegeName(privilege.luid) != "SeChangeNotifyPrivilege") deleted.Add(privilege);
            byte[] sourceDefaultDacl=DefaultDaclBytes(original);
            // DISABLE_MAX_PRIVILEGE ignores the deletion list and leaves reenableable entries behind.
            if (!CreateRestrictedToken(original, 4, 1, disable, (uint)deleted.Count, deleted.ToArray(), 0, IntPtr.Zero, out restricted)) throw new Win32Exception();
            ConfigurePrivateDefaultDacl(restricted);
            if (Convert.ToBase64String(sourceDefaultDacl) != Convert.ToBase64String(DefaultDaclBytes(original))) throw new Exception("restricted fixture changed the source default DACL");
            IntPtr user=TokenBuffer(restricted, 1);
            try { if (!SetTokenInformation(restricted, 4, user, IntPtr.Size)) throw new Win32Exception(); }
            finally { Marshal.FreeHGlobal(user); }
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
            IntPtr actualToken;
            if (!OpenProcessToken(created.process, 11, out actualToken)) throw new Win32Exception();
            try { AssertChildToken(actualToken, (int)created.pid, true); } finally { CloseHandle(actualToken); }
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
