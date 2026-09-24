using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Data;
using System.Windows.Markup;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Xml.Linq;
using AgentDock.ControlPanel;

internal static class Program
{
    private static int _assertions;
    private static readonly List<object> Samples = [];
    private static void Check(bool condition, string message)
    {
        _assertions++;
        if (!condition) throw new InvalidOperationException(message);
    }
    private static JsonElement Json(object value) => JsonSerializer.SerializeToElement(value);
    private static T Named<T>(Window window, string name) where T : class => window.FindName(name) as T ?? throw new InvalidOperationException("Missing control: " + name);
    private static IEnumerable<DependencyObject> Descendants(DependencyObject root)
    {
        yield return root;
        for (var index = 0; index < VisualTreeHelper.GetChildrenCount(root); index++)
            foreach (var child in Descendants(VisualTreeHelper.GetChild(root,index))) yield return child;
    }
    private static void DetachRuntimeStartup(Window window)
    {
        var method = window.GetType().GetMethod("Window_Loaded", BindingFlags.NonPublic | BindingFlags.Instance);
        if (method is not null) window.RemoveHandler(FrameworkElement.LoadedEvent, Delegate.CreateDelegate(typeof(RoutedEventHandler),window,method));
    }
    [STAThread]
    private static int Main(string[] args)
    {
        // This executable must never become an alternative production launcher.
        // Only the GitHub runner may execute it; local development can build it.
        if (Environment.GetEnvironmentVariable("GITHUB_ACTIONS") != "true")
        {
            Console.Error.WriteLine("Offscreen WPF validation is restricted to GitHub Actions. No window or runtime was started.");
            return 2;
        }
        var source = Path.GetFullPath(args.Length > 0 ? args[0] : ".");
        var output = Path.GetFullPath(args.Length > 1 ? args[1] : "dist/layout-validation");
        Directory.CreateDirectory(output);
        var app = new System.Windows.Application { ShutdownMode = ShutdownMode.OnExplicitShutdown };
        // Load real application styles without constructing App or executing its
        // startup, singleton, tray, service, or installer paths.
        var appPath = Path.Combine(source,"desktop/windows/control-panel/App.xaml");
        XNamespace presentation = "http://schemas.microsoft.com/winfx/2006/xaml/presentation";
        var dictionary = XDocument.Load(appPath).Root!.Element(presentation+"Application.Resources")!.Element(presentation+"ResourceDictionary")!;
        var context = new ParserContext { BaseUri = new Uri(appPath) };
        context.XmlnsDictionary[""] = presentation.NamespaceName;
        context.XmlnsDictionary["x"] = "http://schemas.microsoft.com/winfx/2006/xaml";
        app.Resources = (ResourceDictionary)XamlReader.Parse(dictionary.ToString(), context);
        try
        {
            foreach (var theme in new[] {"light","dark","system"})
            {
                var root = Path.Combine(Path.GetTempPath(),"agentdock-layout-"+Guid.NewGuid().ToString("N"));
                Directory.CreateDirectory(root);
                File.WriteAllText(Path.Combine(root,"execution-center-settings.json"),JsonSerializer.Serialize(new { theme }));
                using var runtime = new RuntimeService(root);
                var window = new ExecutionWindow(runtime);
                DetachRuntimeStartup(window);
                window.Width = 1180; window.Height = 780;
                Populate(window);
                foreach (var mode in new[] {"compact","detailed"})
                {
                    Named<ListBox>(window,"CallsList").ItemTemplate = (DataTemplate)window.FindResource(mode=="compact" ? "CallRowTemplate" : "DetailedCallRowTemplate");
                    Named<Button>(window,"CallPresentationButton").Content = mode=="compact" ? "简洁" : "详细";
                    var header = window.FindName("DetailedCallsHeader") as FrameworkElement;
                    if (header is not null) header.Visibility = mode=="detailed" ? Visibility.Visible : Visibility.Collapsed;
                    foreach (var scale in new[] {1.0,1.25,1.5,2.0}) Render(window,output,$"execution-{theme}-{mode}",scale);
                }
                // Expanding/collapsing is driven through the production bound
                // key. No backend request is permitted in this unstarted view.
                var key = window.Objects.First().WorkspaceKey;
                key.IsExpanded = false;
                Layout(window);
                var expander = Descendants((FrameworkElement)window.Content).OfType<Expander>().First(item => item.DataContext is CollectionViewGroup group && ReferenceEquals(group.Name,key));
                Check(!expander.IsExpanded,"Programmatic project collapse did not reach its template.");
                key.IsExpanded = true;
                Layout(window);
                Check(expander.IsExpanded,"Project binding did not restore expansion.");
                Check(Math.Abs(ExecutionLayout.MoreRowHeight*2-ExecutionLayout.ConversationRowHeight)<0.001,"More row is not half-height.");
                var normal = Descendants((FrameworkElement)window.Content).OfType<ListBoxItem>().FirstOrDefault(item => item.DataContext is ExecutionObject { IsGroupFooter:false });
                var more = Descendants((FrameworkElement)window.Content).OfType<ListBoxItem>().FirstOrDefault(item => item.DataContext is ExecutionObject { IsGroupFooter:true,HasMore:true });
                Check(normal is not null && more is not null,"Navigation row containers were not generated.");
                Check(Math.Abs(normal!.ActualHeight-2*more!.ActualHeight)<0.1,"Actual ellipsis layout is not half a conversation row.");
                Check(Named<TextBox>(window,"RequestPayloadText").Text.Contains("workdir"),"Real request was not rendered.");
                Check(Named<TextBox>(window,"ResponsePayloadText").Text.Contains("structured_content"),"Real structured output was not rendered.");
                Check(window.FindName("ConnectionButton") is null && window.FindName("OlderCallsButton") is null && window.FindName("CompactCallsChoice") is null,"Removed toolbar controls still exist.");
                window.Close();
                var panel = new MainWindow(runtime);
                DetachRuntimeStartup(panel);
                var pages = Named<TabControl>(panel,"MainPages");
                var tabs = pages.Items.Cast<TabItem>().ToArray();
                Check(tabs.Select(tab => tab.Tag?.ToString()).SequenceEqual(new[] {"overview","public-access","capabilities","runtime","advanced"}),"Control panel routes differ from the approved pages.");
                pages.SelectedItem = tabs.Single(tab => (string)tab.Tag=="runtime");
                foreach (var scale in new[] {1.0,1.25,1.5,2.0}) Render(panel,output,$"settings-{theme}",scale);
                var groupBox = Named<GroupBox>(panel,"DisplayAndThemeGroup");
                groupBox.ApplyTemplate();
                var outlines = Descendants(groupBox).OfType<Border>().Where(border => border.BorderThickness.Left>0).ToArray();
                // Descendant input outlines have different templates; the group
                // itself must have exactly one nonzero template-owned border.
                var own = outlines.Where(border => ReferenceEquals(border.TemplatedParent,groupBox)).ToArray();
                Check(own.Length==1 && own[0].BorderThickness==new Thickness(2),"GroupBox must have one 2-DIP outline.");
                Check(Named<ComboBox>(panel,"ThemePreferenceCombo") is not null,"Theme preference was lost during migration.");
                typeof(MainWindow).GetMethod("CloseForReplacement", BindingFlags.Instance | BindingFlags.NonPublic)!.Invoke(panel,null);
                Directory.Delete(root,true);
            }
            File.WriteAllText(Path.Combine(output,"layout-validation.json"),JsonSerializer.Serialize(new { assertions=_assertions, samples=Samples, mode="offscreen-wpf", runtime_started=false, installer_started=false, physical_monitor_dpi_test=false },new JsonSerializerOptions{WriteIndented=true}));
            Console.WriteLine($"Offscreen WPF regression passed: {_assertions} assertions, {Samples.Count} rendered samples; no runtime, tray, installer, or visible window started.");
            return 0;
        }
        catch(Exception error) { Console.Error.WriteLine(error); return 1; }
        finally { app.Shutdown(); }
    }
    private static void Populate(ExecutionWindow window)
    {
        var key = new WorkspaceGroupKey("fixture","AgentDock");
        key.Apply(Json(new{title="AgentDock",root="C:/isolated-fixture",total=32,recent_count=2,mode="history",last_activity_at=DateTimeOffset.UtcNow}));
        key.IsExpanded=true;
        for(var index=0;index<5;index++) window.Objects.Add(new ExecutionObject{Id="conversation-"+index,Title=index==0?"修复工具响应与调用记录":"独立测试对话 "+index,WorkspaceKey=key,RecentlyActive=index<2});
        window.Objects.Add(new ExecutionObject{Id="footer:fixture",IsGroupFooter=true,HasMore=true,WorkspaceKey=key});
        var collapsed=new WorkspaceGroupKey("inactive","历史项目");
        collapsed.Apply(Json(new{title="历史项目",total=4,recent_count=0,mode="auto"}));
        window.Objects.Add(new ExecutionObject{Id="footer:inactive",IsGroupFooter=true,HasMore=false,WorkspaceKey=collapsed});
        var request="{\n  \"workdir\": \"C:/isolated-fixture\"\n}";
        var output="{\n  \"structured_content\": {\"status\": \"ok\", \"items\": 200},\n  \"content\": [{\"type\": \"text\", \"text\": \"工具输出完整保存。\"}]\n}";
        var row=new ExecutionCallRow(Json(new{call_id="call-fixture",conversation_id="conversation-0",tool_name="agentdock_context",display_title="加载上下文",status="succeeded",rpc_elapsed_ms=123,request_received_at=DateTimeOffset.UtcNow,request=new{state="complete",preview=request,bytes=request.Length,lines=3},response=new{state="complete",preview=output,bytes=output.Length,lines=4}}));
        window.Calls.Add(row);
        window.Calls.Add(new ExecutionCallRow(Json(new{tool_name="file_edit",display_title="更新 src/example.go",status="succeeded",rpc_elapsed_ms=218,request_received_at=DateTimeOffset.UtcNow,file_edit=new{stats_state="known",insertions=26,deletions=9}})));
        window.Calls.Add(new ExecutionCallRow(Json(new{tool_name="mcp_tool_call",display_title="读取服务状态",status="running",request_received_at=DateTimeOffset.UtcNow})));
        Named<TextBlock>(window,"ObjectTitle").Text="修复工具响应与调用记录";
        Named<FrameworkElement>(window,"EmptyPanel").Visibility=Visibility.Collapsed;
        Named<FrameworkElement>(window,"SidebarEmpty").Visibility=Visibility.Collapsed;
        var details=Named<FrameworkElement>(window,"DetailsPanel");details.Height=280;details.Visibility=Visibility.Visible;
        Named<FrameworkElement>(window,"InsertionPanel").Visibility=Visibility.Collapsed;
        var tabs=Named<TabControl>(window,"CallDetailsTabs");tabs.DataContext=row;tabs.Visibility=Visibility.Visible;
    }
    private static FrameworkElement Layout(Window window)
    {
        var root=(FrameworkElement)window.Content;
        root.Measure(new Size(1180,780));root.Arrange(new Rect(0,0,1180,780));root.UpdateLayout();
        return root;
    }
    private static void Render(Window window,string output,string name,double scale)
    {
        var root=Layout(window);
        Check(root.ActualWidth>0 && root.ActualHeight>0,"Empty layout surface.");
        foreach(var element in Descendants(root).OfType<FrameworkElement>()) Check(double.IsFinite(element.ActualWidth) && double.IsFinite(element.ActualHeight),"Non-finite layout size.");
        var image=new RenderTargetBitmap((int)(1180*scale),(int)(780*scale),96*scale,96*scale,PixelFormats.Pbgra32);
        image.Render(root);var encoder=new PngBitmapEncoder();encoder.Frames.Add(BitmapFrame.Create(image));
        var path=Path.Combine(output,name+"-"+(int)(scale*100)+".png");using(var stream=File.Create(path))encoder.Save(stream);
        Samples.Add(new{name,render_scale=scale,width=image.PixelWidth,height=image.PixelHeight,bytes=new FileInfo(path).Length});
    }
}
