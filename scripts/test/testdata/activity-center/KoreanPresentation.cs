using System.Globalization;
using System.Resources;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private static void TestKoreanPresentation()
    {
        var count=0;
        void Check(bool yes,string message) {count++;if(!yes)throw new InvalidOperationException(message);}
        JsonElement Json(object value) => JsonSerializer.SerializeToElement(value);
        string Hash(string raw) => Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(raw))).ToLowerInvariant();
        var resources=new ResourceManager("AgentDock.ControlPanel.Resources.UiStrings",typeof(ExecutionCallRow).Assembly);
        var english=resources.GetResourceSet(CultureInfo.GetCultureInfo("en"),true,true)!;
        var keys=english.Cast<System.Collections.DictionaryEntry>().ToDictionary(pair=>(string)pair.Key,pair=>(string)pair.Value!);
        Check(keys.Count>=1000,"resource inventory unexpectedly small");
        var slots=new Regex(@"\{\d+(?:,[^}:]+)?(?::[^}]+)?\}");
        foreach(var locale in new[]{"en","zh-CN","ko-KR"})
        {
            UiText.ApplyPreference(locale);
            var localized=resources.GetResourceSet(CultureInfo.GetCultureInfo(locale),true,false);
            Check(localized is not null,"missing real satellite assembly: "+locale);
            foreach(var item in keys)
            {
                var text=UiText.Get(item.Key);
                Check(localized!.GetString(item.Key) is not null && text == localized.GetString(item.Key),"unresolved key "+locale+"/"+item.Key);
                Check(slots.Matches(item.Value).Select(m=>m.Value).Order().SequenceEqual(slots.Matches(text).Select(m=>m.Value).Order()),"placeholder mismatch "+locale+"/"+item.Key);
            }
            var original="读取文件 · 사용자文件.txt";
            var descriptor=new OwnedTextDescriptor{SchemaVersion=1,Code="tool.read_file",Args=[" · 사용자文件.txt"],TextHash=Hash(original)};
            var value=Json(new {tool_name="read_file",activity_label_source="tool",display_title=original,summary=original,title_text=descriptor,summary_text=descriptor,status="succeeded",updated_seq=1});
            var row=new ExecutionCallRow(value);
            Check(row.Title=="read_file · "+UiText.Format("OwnedTool_read_file"," · 사용자文件.txt"),"generated label not translated");
            Check(row.Summary==UiText.Format("OwnedTool_read_file"," · 사용자文件.txt"),"summary not translated");
            Check(row.Technical.Contains("text_hash") && value.Text("display_title")==original,"stored original was changed");
            var decoded=JsonSerializer.Deserialize<ActivityEvent>(JsonSerializer.Serialize(new{title=original,title_text=descriptor,activity_label_source="tool",tool_name="read_file",kind="call.completed",status="succeeded"}),ActivityClient.JsonOptions)!;
            var activity=new ActivityRow(decoded);Check(activity.Title==row.Summary,"activity and execution use different descriptors");
            foreach(var bad in new[]{new OwnedTextDescriptor{SchemaVersion=2,Code=descriptor.Code,Args=descriptor.Args,TextHash=descriptor.TextHash},new(){SchemaVersion=1,Code="tool.unknown",Args=[],TextHash=descriptor.TextHash},new(){SchemaVersion=1,Code=descriptor.Code,Args=null,TextHash=descriptor.TextHash},new(){SchemaVersion=1,Code=descriptor.Code,Args=["x","y"],TextHash=descriptor.TextHash},new(){SchemaVersion=1,Code=descriptor.Code,Args=["x"],TextHash="stale"}})
                Check(OwnedText.Render(bad,original,"read_file","tool","succeeded")==original,"malformed/future descriptor rewrote original");
            Check(OwnedText.Render(descriptor,original,"read_file","user","succeeded")==original,"user label translated by coincidence");
            Check(OwnedText.Render(descriptor,original,"third:read_file","tool","succeeded")==original,"third-party label translated");
            Check(ExecutionObject.From(Json(new{conversation_id="conv-user",title="新对话",title_source="user"}),"conversation").Title=="新对话","user title changed");
            var command=new ActivityEvent{Kind="task.completed",Title="task.user-title",Status="succeeded"};
            Check(ActivityPresentation.EventHeading(command,command.Title).Contains(command.Title),"user title resembling event code hidden");
            var failed="失败：원문 실제 오류";
            row.Apply(Json(new{tool_name="read_file",display_title=original,summary=failed,status="failed",updated_seq=2}));
            Check(row.Summary==failed,"new failure masked by stale success text");
            var titleRaw="修改执行权限";
            Check(OwnedText.Render(new OwnedTextDescriptor{SchemaVersion=1,Code="permission.update",TextHash=Hash(titleRaw)},titleRaw,"permission.update","","succeeded")==UiText.Get("OwnedPermissionUpdate"),"zero-argument permission descriptor failed");
            var permission="scope=workspace scope_id=wsp_fixture mode=rules revision=2；操作系统权限未改变。";
            var permissionText=new OwnedTextDescriptor{SchemaVersion=1,Code="permission.updated",Args=["workspace","wsp_fixture","rules","2"],TextHash=Hash(permission)};
            Check(OwnedText.Render(permissionText,permission,"permission.update","","succeeded")==UiText.Format("OwnedPermissionUpdated","workspace","wsp_fixture","rules","2"),"permission summary not localized");
            Check(OwnedText.Render(permissionText,permission,"permission.update","","failed")==permission,"permission failure changed to success");
            var payload=new ExecutionPayloadView("response");payload.Describe(Json(new{state="partial",@ref="blob",bytes=9,lines=2}));
            Check(payload.StateLabel==UiText.Get("ExecutionOutputPartial"),"payload state is not localized");
            var request="原始cmd -x / 사용자.txt";
            Check(payload.ApplyPage(Json(new{payload=new{@ref="blob"},offset=0,next_offset=9,has_more=false,text=request}),"blob",false) && payload.Text==request,"payload content modified");
            var edit=new ExecutionCallRow(Json(new{tool_name="file_edit",file_edit=new{stats_state="known",insertions=26,deletions=9,path="保留/한글.txt",diff_preview="- 原文\n+ 사용자",changed=true}}));
            Check(edit.AddedLinesText=="+26"&&edit.DeletedLinesText=="−9"&&edit.FileEditDetails.Contains("- 原文\n+ 사용자"),"file statistics or original diff changed");
            var unknown=new ExecutionCallRow(Json(new{tool_name="file_edit",file_edit=new{stats_state="unknown"}}));Check(unknown.AddedLinesText=="—","unknown edit treated as zero");
            var diagnostic="task_owner_mismatch: 原始句子 사용자";
            Check(NativeDiagnosticText.Describe(diagnostic).Contains(UiText.Get("NativeTaskOwnerMismatch"))&&NativeDiagnosticText.Describe(diagnostic).EndsWith(diagnostic),"native failure lost original");
            if(locale=="ko-KR")foreach(var key in new[]{"ExecutionInsertionHelp","ExecutionInsertionInactive","ExecutionInsertionPending","ExecutionInsertionReserved","ExecutionInsertionAttached","ExecutionInsertionExpired","ExecutionInsertionCancelled","ExecutionInsertionDeliveryUnknown","ExecutionRequestAndOutput","ActivitySummaryCounts","LocalHealthyPublicUnavailable","NativeElevatedUnavailable"})Check(Regex.IsMatch(UiText.Get(key),"[가-힣]"),"Korean message missing: "+key);
        }
        Check(UiText.ResolveLocale("system","ko") == "ko-KR" && UiText.ResolveLocale("system","ko-KR") == "ko-KR","neutral Korean not resolved");
        Check(UiText.ResolveLocale("system","fr-FR") == "en","unknown system culture did not use English");
        UiText.ApplyPreference("en");
        Console.WriteLine($"Korean/English/Chinese real product resource and presentation regression: {count} assertions passed. No user settings, network, runtime, window or installer were opened.");
    }
}
