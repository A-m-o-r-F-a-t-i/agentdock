using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

namespace AgentDock.Testing
{
    public sealed class ConsoleWindowEvent
    {
        public string TimestampUtc { get; set; }
        public long WindowHandle { get; set; }
        public uint ProcessId { get; set; }
        public string WindowClass { get; set; }
        public string Title { get; set; }
        public string ProcessPath { get; set; }
    }

    // Observes the current interactive desktop only. It does not create a
    // console, move windows or synthesize input. Existing visible consoles are
    // excluded; SHOW events and 20ms scans catch newly visible console windows.
    public sealed class ConsoleWindowWatch : IDisposable
    {
        private readonly ConcurrentQueue<ConsoleWindowEvent> events = new ConcurrentQueue<ConsoleWindowEvent>();
        private readonly HashSet<IntPtr> baseline = new HashSet<IntPtr>();
        private readonly HashSet<IntPtr> seen = new HashSet<IntPtr>();
        private readonly ManualResetEventSlim initialized = new ManualResetEventSlim(false);
        private readonly Thread thread;
        private readonly WinEventCallback callback;
        private GCHandle callbackRoot;
        private Exception failure;
        private uint threadId;
        private int disposed;

        public ConsoleWindowWatch()
        {
            callback = OnWindowEvent;
            callbackRoot = GCHandle.Alloc(callback);
            thread = new Thread(MessageLoop) { IsBackground = true, Name = "AgentDock console-window regression observer" };
            thread.SetApartmentState(ApartmentState.STA);
            thread.Start();
            if (!initialized.Wait(TimeSpan.FromSeconds(5))) { Dispose(); throw new TimeoutException("Console observer did not initialize."); }
            if (failure != null) { Dispose(); throw new InvalidOperationException("Console observer initialization failed.", failure); }
        }

        public ConsoleWindowEvent[] Snapshot() { return events.ToArray(); }
        public string Failure { get { return failure == null ? null : failure.ToString(); } }

        // Positive-control the actual WinEvent delivery path using a one-pixel
        // tool window outside the screen, with no console process or activation.
        // This runs before the real installation observation begins.
        public static void ValidateEventDelivery()
        {
            WindowProcedure procedure = delegate(IntPtr window, uint message, UIntPtr wParam, IntPtr lParam) { return DefWindowProc(window, message, wParam, lParam); };
            IntPtr instance = GetModuleHandle(null);
            var windowClass = new WindowClass { Size = (uint)Marshal.SizeOf(typeof(WindowClass)), Procedure = Marshal.GetFunctionPointerForDelegate(procedure), Instance = instance, Name = "ConsoleWindowClass" };
            ushort atom = RegisterClassEx(ref windowClass);
            if (atom == 0) throw new Win32Exception(Marshal.GetLastWin32Error(), "Register hidden observer fixture.");
            IntPtr hidden = IntPtr.Zero;
            try
            {
                using (var observer = new ConsoleWindowWatch())
                {
                    hidden = CreateWindowEx(0x08000080, windowClass.Name, "AgentDock offscreen observer self-test", 0x80000000, -32000, -32000, 1, 1, IntPtr.Zero, IntPtr.Zero, instance, IntPtr.Zero);
                    if (hidden == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error());
                    ShowWindow(hidden, 4);
                    NotifyWinEvent(0x8002, hidden, 0, 0);
                    DateTime deadline = DateTime.UtcNow.AddSeconds(2);
                    while (DateTime.UtcNow < deadline && observer.Snapshot().Length == 0) Thread.Sleep(10);
                    if (observer.Failure != null) throw new InvalidOperationException(observer.Failure);
                    bool found = false;
                    foreach (var item in observer.Snapshot()) if (item.WindowHandle == hidden.ToInt64()) found = true;
                    if (!found) throw new InvalidOperationException("Console observer did not receive its positive-control event.");
                }
            }
            finally
            {
                if (hidden != IntPtr.Zero) DestroyWindow(hidden);
                UnregisterClass(windowClass.Name, instance);
                GC.KeepAlive(procedure);
            }
        }

        private void MessageLoop()
        {
            IntPtr hook = IntPtr.Zero;
            UIntPtr timer = UIntPtr.Zero;
            try
            {
                threadId = GetCurrentThreadId();
                EnumWindows(delegate(IntPtr window, IntPtr ignored) { if (IsWindowVisible(window)) baseline.Add(window); return true; }, IntPtr.Zero);
                hook = SetWinEventHook(0x8002, 0x8002, IntPtr.Zero, callback, 0, 0, 0);
                if (hook == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error());
                timer = SetTimer(IntPtr.Zero, UIntPtr.Zero, 20, IntPtr.Zero);
                if (timer == UIntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error());
                initialized.Set();
                Message message;
                int result;
                while ((result = GetMessage(out message, IntPtr.Zero, 0, 0)) > 0)
                {
                    if (message.Id == 0x0113) EnumWindows(delegate(IntPtr window, IntPtr ignored) { Record(window, false); return true; }, IntPtr.Zero);
                    TranslateMessage(ref message);
                    DispatchMessage(ref message);
                }
                if (result < 0) throw new Win32Exception(Marshal.GetLastWin32Error());
            }
            catch (Exception error) { failure = error; initialized.Set(); }
            finally
            {
                if (timer != UIntPtr.Zero) KillTimer(IntPtr.Zero, timer);
                if (hook != IntPtr.Zero) UnhookWinEvent(hook);
            }
        }

        private void OnWindowEvent(IntPtr hook, uint eventType, IntPtr window, int objectId, int childId, uint eventThread, uint eventTime)
        {
            if (objectId != 0 || childId != 0) return;
            // A SHOW event remains evidence even when a very short-lived window
            // is hidden again before this out-of-process callback is dispatched.
            try { Record(window, true); }
            catch (Exception error) { failure = error; }
        }

        private void Record(IntPtr window, bool shownEvent)
        {
            if (window == IntPtr.Zero || baseline.Contains(window) || seen.Contains(window)) return;
            if (!shownEvent && !IsWindowVisible(window)) return;
            var className = new StringBuilder(256);
            GetClassName(window, className, className.Capacity);
            string name = className.ToString();
            if (name != "ConsoleWindowClass" && name != "CASCADIA_HOSTING_WINDOW_CLASS") return;
            seen.Add(window);
            uint processId;
            GetWindowThreadProcessId(window, out processId);
            var title = new StringBuilder(1024);
            // GetWindowText synchronously messages another thread for windows
            // in this process. Avoid blocking the event loop on a test/client
            // thread; foreign-process captions use the system's cached text.
            if (processId != GetCurrentProcessId()) GetWindowText(window, title, title.Capacity);
            string path = null;
            IntPtr process = OpenProcess(0x1000, false, processId);
            if (process != IntPtr.Zero)
            {
                try
                {
                    var image = new StringBuilder(32768);
                    uint size = (uint)image.Capacity;
                    if (QueryFullProcessImageName(process, 0, image, ref size)) path = image.ToString();
                }
                finally { CloseHandle(process); }
            }
            events.Enqueue(new ConsoleWindowEvent { TimestampUtc = DateTime.UtcNow.ToString("o"), WindowHandle = window.ToInt64(), ProcessId = processId, WindowClass = name, Title = title.ToString(), ProcessPath = path });
        }

        public void Dispose()
        {
            if (Interlocked.Exchange(ref disposed, 1) != 0) return;
            if (threadId != 0) PostThreadMessage(threadId, 0x0012, UIntPtr.Zero, IntPtr.Zero);
            if (!thread.Join(TimeSpan.FromSeconds(5))) throw new TimeoutException("Console observer did not exit.");
            if (callbackRoot.IsAllocated) callbackRoot.Free();
            initialized.Dispose();
        }

        private delegate bool EnumWindowsCallback(IntPtr window, IntPtr parameter);
        private delegate IntPtr WindowProcedure(IntPtr window, uint message, UIntPtr wParam, IntPtr lParam);
        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)] private struct WindowClass { public uint Size; public uint Style; public IntPtr Procedure; public int ClassExtra; public int WindowExtra; public IntPtr Instance; public IntPtr Icon; public IntPtr Cursor; public IntPtr Background; public string MenuName; public string Name; public IntPtr SmallIcon; }
        private delegate void WinEventCallback(IntPtr hook, uint eventType, IntPtr window, int objectId, int childId, uint eventThread, uint eventTime);
        [StructLayout(LayoutKind.Sequential)] private struct Message { public IntPtr Window; public uint Id; public UIntPtr WParam; public IntPtr LParam; public uint Time; public int X; public int Y; public uint Private; }
        [DllImport("user32.dll")] private static extern bool EnumWindows(EnumWindowsCallback callback, IntPtr parameter);
        [DllImport("user32.dll")] private static extern bool IsWindowVisible(IntPtr window);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern int GetClassName(IntPtr window, StringBuilder text, int count);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern int GetWindowText(IntPtr window, StringBuilder text, int count);
        [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);
        [DllImport("user32.dll", SetLastError = true)] private static extern IntPtr SetWinEventHook(uint minimum, uint maximum, IntPtr module, WinEventCallback callback, uint processId, uint threadId, uint flags);
        [DllImport("user32.dll")] private static extern bool UnhookWinEvent(IntPtr hook);
        [DllImport("user32.dll", SetLastError = true)] private static extern UIntPtr SetTimer(IntPtr window, UIntPtr id, uint milliseconds, IntPtr callback);
        [DllImport("user32.dll")] private static extern bool KillTimer(IntPtr window, UIntPtr id);
        [DllImport("user32.dll", SetLastError = true)] private static extern int GetMessage(out Message message, IntPtr window, uint minimum, uint maximum);
        [DllImport("user32.dll")] private static extern bool TranslateMessage(ref Message message);
        [DllImport("user32.dll")] private static extern IntPtr DispatchMessage(ref Message message);
        [DllImport("user32.dll", SetLastError = true)] private static extern bool PostThreadMessage(uint threadId, uint message, UIntPtr wParam, IntPtr lParam);
        [DllImport("kernel32.dll")] private static extern uint GetCurrentThreadId();
        [DllImport("kernel32.dll")] private static extern uint GetCurrentProcessId();
        [DllImport("kernel32.dll", SetLastError = true)] private static extern IntPtr OpenProcess(uint access, bool inherit, uint processId);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern bool QueryFullProcessImageName(IntPtr process, uint flags, StringBuilder path, ref uint size);
        [DllImport("kernel32.dll")] private static extern bool CloseHandle(IntPtr handle);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr GetModuleHandle(string module);
        [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern ushort RegisterClassEx(ref WindowClass windowClass);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern bool UnregisterClass(string name, IntPtr instance);
        [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern IntPtr CreateWindowEx(uint extendedStyle, string className, string title, uint style, int x, int y, int width, int height, IntPtr parent, IntPtr menu, IntPtr instance, IntPtr parameter);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr DefWindowProc(IntPtr window, uint message, UIntPtr wParam, IntPtr lParam);
        [DllImport("user32.dll")] private static extern bool DestroyWindow(IntPtr window);
        [DllImport("user32.dll")] private static extern void NotifyWinEvent(uint eventType, IntPtr window, int objectId, int childId);
        [DllImport("user32.dll")] private static extern bool ShowWindow(IntPtr window, int command);
    }
}
