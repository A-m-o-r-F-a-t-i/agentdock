using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Reflection;
using System.Text;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private static void TestPresentation()
    {
        CultureInfo.CurrentUICulture = CultureInfo.GetCultureInfo("zh-CN");
        var task = new ActivityTask { Id = "tsk_fixture", Status = "completed", Outcome = "cancelled", CancelReason = "设备断开", ArchivedAt = DateTimeOffset.Now };
        Require(task.StateLabel.Contains("已取消") && task.StateLabel.Contains("已归档"), "Archiving overwrote the cancellation result.");
        var thread = new ActivityThread { Id = "main", Status = "closed", Summary = "1" };
        var text = ActivityPresentation.ThreadSummary(task, thread);
        Require(text.Contains("取消原因: 设备断开") && text.Contains("阶段摘要: 1") && text.Contains("尚未记录下一动作"), "Reasons or numeric summary were combined incorrectly.");
        var timeline = new ActivityTimeline();
        var cancelled = Sample(1, "task.completed", ""); cancelled.Title = "task.completed"; cancelled.Status = "cancelled";
        timeline.Apply(cancelled);
        Require(timeline.Rows[0].Heading == "任务已取消", "Raw lifecycle title masked cancellation.");
        timeline.Reset(); timeline.Apply(Sample(1, "command.started"));
        var row = timeline.Rows[0];
        Require(!row.CanStop, "Historical running event was treated as live.");
        row.Observe(new ActivitySession { Status = "running" }, true);
        Require(row.CanStop, "Live command cannot be stopped.");
        row.Observe(null, true); Require(!row.CanStop, "Stale live data remained actionable.");
        row.Observe(new ActivitySession { Status = "running" }, true);
        var done = Sample(2, "command.completed"); done.Status = "success"; timeline.Apply(done);
        Require(!row.CanStop, "A stale live poll overrode the command completion event.");
        task.Status = "blocked"; task.Outcome = ""; task.ArchivedAt = null; task.Blocker = "任务阻塞";
        thread.Status = "blocked"; thread.BlockReason = "分支阻塞";
        text = ActivityPresentation.ThreadSummary(task, thread);
        Require(text.Contains("任务阻塞原因: 任务阻塞") && text.Contains("分支阻塞原因: 分支阻塞"), "Task and branch blockers were conflated.");
    }

    private static void TestAccessModes(string runtimeRoot)
    {
        using var runtime = new RuntimeService(runtimeRoot);
        var window = new MainWindow(runtime);
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.NonPublic;
        void Set(string field, object value) => typeof(MainWindow).GetField(field, flags)!.SetValue(window, value);
        void Invoke(string name, params object[] values) => typeof(MainWindow).GetMethod(name, flags)!.Invoke(window, values);
        var snapshot = new RuntimeSnapshot(new RuntimeManifest(), new ControlPanelSettings(), "1.1.1", true, true, true,
            "http://127.0.0.1:8765/mcp", "https://quick.example", "https://quick.example/mcp", "https://saved.example", "quick",
            false, false, true, new NexusDeviceStatus(false,"","","",false), false, DateTimeOffset.Now);
        Set("_snapshot",snapshot); Set("_tunnelSelectionDirty",false); Set("_namedDraftDirty",false);
        Invoke("ApplySnapshot",snapshot);
        var names = new[] { "Local", "Quick", "Named", "Tailscale" };
        var groups = new[] { "LocalAccessGroup", "QuickAccessGroup", "NamedTunnelGroup", "TailscaleGroup" };
        foreach (var from in names)
        {
            ((RadioButton)window.FindName(from+"ModeRadio")).IsChecked = true;
            foreach (var to in names)
            {
                ((RadioButton)window.FindName(to+"ModeRadio")).IsChecked = true;
                Invoke("UpdateTunnelModeUi");
                for (var index=0;index<names.Length;index++)
                    Require(((GroupBox)window.FindName(groups[index])).Visibility == (names[index]==to ? Visibility.Visible : Visibility.Collapsed), "Mode panel leaked: "+from+" -> "+to);
                Require(((TextBlock)window.FindName("EffectiveAccessText")).Text.Contains("临时"), "Editing changed the effective mode.");
                // Return to the source so all 12 cross-transitions and 4 same-mode cases run.
                ((RadioButton)window.FindName(from+"ModeRadio")).IsChecked = true;
            }
        }
        ((RadioButton)window.FindName("NamedModeRadio")).IsChecked = true;
        var domain=(TextBox)window.FindName("ServerUrlTextBox"); var password=(PasswordBox)window.FindName("TunnelTokenPasswordBox");
        domain.Text="https://draft.example"; password.Password="isolated-replacement-token";
        Invoke("ApplySnapshot",snapshot);
        Require(domain.Text=="https://draft.example" && password.Password=="isolated-replacement-token", "Refresh overwrote the domain/token draft.");
        window.FinishAccessApply(false,"named");
        Require(domain.Text=="https://draft.example" && password.Password=="isolated-replacement-token", "Failed apply discarded its draft.");
        window.FinishAccessApply(true,"quick");
        Require(password.Password=="isolated-replacement-token", "Applying another provider discarded the hidden draft.");
        Set("_tunnelChangeInProgress",true); Invoke("UpdateTunnelModeUi");
        Require(!((Button)window.FindName("ApplyAccessButton")).IsEnabled && !((RadioButton)window.FindName("QuickModeRadio")).IsEnabled, "Concurrent access mode changes were not disabled.");
        Set("_tunnelChangeInProgress",false); window.FinishAccessApply(true,"named");
        Require(password.Password.Length==0, "Successful named apply retained replacement credential input.");
        window.CloseForReplacement();
    }

    private static async Task TestPublicDiscoveryAsync()
    {
        const string origin="https://fixture.example";
        const string metadata=origin+"/.well-known/oauth-protected-resource/mcp";
        foreach(var parameters in new[] { "resource_metadata=\""+metadata+"\"", "scope=\"mcp\", resource_metadata = \""+metadata+"\", error_description=\"login, retry\"" })
            Require(RuntimeService.TryResourceMetadata(parameters,out var found) && found==metadata,"A valid challenge was rejected.");
        Require(!RuntimeService.TryResourceMetadata("resource_metadata=\"a\", RESOURCE_METADATA=\"b\"",out _),"Ambiguous challenge accepted.");
        var mode="success";
        using var client=new HttpClient(new ProbeHandler(request =>
        {
            Require(request.Headers.Authorization is null,"Anonymous discovery leaked a credential.");
            var path=request.RequestUri!.AbsolutePath;
            if(mode=="network")throw new HttpRequestException("isolated TLS transport failure");
            if(mode=="redirect")return new HttpResponseMessage(HttpStatusCode.Redirect);
            var response=new HttpResponseMessage(path=="/mcp" ? HttpStatusCode.Unauthorized : HttpStatusCode.OK);
            if(path=="/mcp")response.Headers.TryAddWithoutValidation("WWW-Authenticate", "Bearer scope=\"mcp\", resource_metadata=\""+metadata+"\"");
            object body=path switch {
                "/.well-known/oauth-protected-resource/mcp" => new { resource=origin+"/mcp",authorization_servers=new[]{origin} },
                "/.well-known/oauth-authorization-server" => new { issuer=mode=="wrong-origin" ? "https://stale.example" : origin,
                    authorization_endpoint=origin+"/oauth/authorize",token_endpoint=origin+"/oauth/token",registration_endpoint=origin+"/register",
                    code_challenge_methods_supported=new[]{"S256"},grant_types_supported=new[]{"authorization_code","refresh_token"} },
                _ => new { ok=true }
            };
            response.Content=new StringContent(JsonSerializer.Serialize(body),Encoding.UTF8,"application/json");return response;
        }));
        var passed=await RuntimeService.CheckPublicDiscoveryAsync(client,origin,CancellationToken.None);
        Require(passed.Success && passed.Message.Contains("仍需"),"Server checks claimed completed client authorization or failed discovery.");
        foreach(var failure in new[]{"network","redirect","wrong-origin"})
        {
            mode=failure;var result=await RuntimeService.CheckPublicDiscoveryAsync(client,origin,CancellationToken.None);
            Require(!result.Success && result.Message.Contains(origin),"Discovery failure omitted the configuration or passed: "+failure);
        }
    }
    private sealed class ProbeHandler(Func<HttpRequestMessage,HttpResponseMessage> respond):HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request,CancellationToken cancellationToken)=>Task.FromResult(respond(request));
    }
}
