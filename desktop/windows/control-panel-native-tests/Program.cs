using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Security.Principal;
using System.Text.Json;
using AgentDock.ControlPanel;

internal static class Program
{
    private static int _assertions;
    private static readonly List<object> Evidence = [];
    private static void Check(bool value, string message) { _assertions++; if (!value) throw new InvalidOperationException(message); }
    [STAThread]
    private static int Main(string[] args)
    {
        if (Environment.GetEnvironmentVariable("GITHUB_ACTIONS") != "true" || Environment.GetEnvironmentVariable("AGENTDOCK_NATIVE_ACCEPTANCE") != "1")
        { Console.Error.WriteLine("Native acceptance requires an explicitly enabled isolated GitHub runner."); return 2; }
        var temp = Environment.GetEnvironmentVariable("RUNNER_TEMP") ?? throw new InvalidOperationException("Missing isolated temp root");
        using var identity = WindowsIdentity.GetCurrent();
        Check(new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator), "Runner administrator token required");
        var root = Path.Combine(temp,"agentdock-native-"+Guid.NewGuid().ToString("N")); Directory.CreateDirectory(root);
        var output = Path.GetFullPath(args.Length == 0 ? "dist/native-validation" : args[0]); Directory.CreateDirectory(output);
        var schedulerType = Type.GetTypeFromProgID("Schedule.Service") ?? throw new InvalidOperationException("Native Task Scheduler missing");
        dynamic service = Activator.CreateInstance(schedulerType)!; service.Connect(); dynamic folder = service.GetFolder("\\");
        try
        {
            foreach (var scenario in new[] { "success", "prepare", "apply", "verify", "cancel", "restore", "verify_restored", "native_unknown", "tampered_definition" })
                RunScenario(service,folder,identity,root,output,scenario);
            var report = new { assertions=_assertions, platform=RuntimeInformation.OSDescription, architecture=RuntimeInformation.ProcessArchitecture.ToString(), isolation="GitHub hosted runner", scheduler="native COM", recovery_files="native NTFS", ui_started=false, production_runtime_started=false, scenarios=Evidence };
            File.WriteAllText(Path.Combine(output,"native-privilege-validation.json"),JsonSerializer.Serialize(report,new JsonSerializerOptions{WriteIndented=true}));
            Console.WriteLine($"Native privilege recovery passed: {_assertions} assertions, {Evidence.Count} scenarios.");
            return 0;
        }
        catch(Exception error){Console.Error.WriteLine(error);return 1;}
        finally
        {
            Marshal.FinalReleaseComObject(folder);Marshal.FinalReleaseComObject(service);
            if(Directory.Exists(root))Directory.Delete(root,true);
        }
    }

    private static void RunScenario(dynamic service,dynamic folder,WindowsIdentity identity,string root,string output,string scenario)
    {
        var taskName="AgentDock-Acceptance-"+Guid.NewGuid().ToString("N");
        var directory=Path.Combine(root,scenario);Directory.CreateDirectory(directory);
        var recovery=Path.Combine(directory,"recovery");var manifest=Path.Combine(directory,"runtime-state.json");File.WriteAllText(manifest,"original");
        dynamic definition=service.NewTask(0);
        definition.Principal.UserId=identity.User!.Value;definition.Principal.LogonType=3;definition.Principal.RunLevel=0;
        definition.Settings.Enabled=false;
        dynamic action=definition.Actions.Create(0);action.Path=Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System),"cmd.exe");action.Arguments="/d /c exit 0";
        dynamic original=folder.RegisterTaskDefinition(taskName,definition,6,identity.User.Value,null,3,null);original.Enabled=false;
        string oldXml=original.Xml;
        int restoreCount=0;Exception? failure=null;Process? unknown=null;
        using var cancelled=new CancellationTokenSource();
        void Native(string kind)
        {
            var result=TaskAdminService.Run(["--task-admin",kind,"--task-name",taskName,"--backup-directory",recovery,"--runtime-root",directory,"--launcher-path",Path.Combine(directory,"fixture-never-started.exe"),"--user-sid",identity.User.Value,"--user-name",identity.Name]);
            if(result!=0)throw new IOException("Native TaskAdmin "+kind+" failed");
        }
        try
        {
            var actions=new PrivilegeTransitionActions(
                async token=>
                {
                    Native("prepare-elevated");TaskAdminService.VerifyCurrentTask(taskName,true,false);
                    Check(File.Exists(Path.Combine(recovery,"state.json")) && File.Exists(Path.Combine(recovery,"task.xml")),"Native backup precedes mutations");
                    if(scenario=="native_unknown")
                    {
                        var start=new ProcessStartInfo("powershell.exe"){UseShellExecute=false,CreateNoWindow=true};foreach(var value in new[]{"-NoLogo","-NoProfile","-NonInteractive","-Command","Start-Sleep -Seconds 120"})start.ArgumentList.Add(value);
                        unknown=Process.Start(start)??throw new IOException("Could not create isolated native waiting process");
                        cancelled.Cancel();
                        var method=typeof(RuntimeService).GetMethod("WaitForNativeExitAsync",BindingFlags.NonPublic|BindingFlags.Static)!;
                        await (Task)method.Invoke(null,[unknown,token])!;
                    }
                    if(scenario=="cancel"){cancelled.Cancel();token.ThrowIfCancellationRequested();}
                    if(scenario=="prepare")throw new IOException("Injected failure after real native preparation");
                },
                token=>{RecoveryFiles.WriteText(manifest,"new");if(scenario is "apply" or "restore" or "verify_restored" or "tampered_definition")throw new IOException("Injected manifest boundary failure");return Task.CompletedTask;},
                token=>{TaskAdminService.VerifyCurrentTask(taskName,true,false);Check(File.ReadAllText(manifest)=="new","Forward manifest state");if(scenario=="verify")throw new IOException("Injected forward verification failure");return Task.CompletedTask;},
                token=>{restoreCount++;Check(!token.IsCancellationRequested,"Recovery token independent of user cancellation");if(scenario=="restore")throw new IOException("Injected native restore failure");Native("restore");RecoveryFiles.WriteText(manifest,"original");if(scenario=="tampered_definition"){dynamic restored=folder.GetTask(taskName);dynamic altered=restored.Definition;altered.Actions.Item(1).Arguments="/d /c exit 7";folder.RegisterTaskDefinition(taskName,altered,6,identity.User.Value,null,3,null);}return Task.CompletedTask;},
                token=>{TaskAdminService.VerifyRestoredBackup(taskName,recovery);Check(File.ReadAllText(manifest)=="original","Original manifest restored");if(scenario=="verify_restored")throw new IOException("Injected recovered verification failure");return Task.CompletedTask;},
                ()=>File.Exists(Path.Combine(recovery,"state.json")));
            try{PrivilegeTransition.RunAsync(recovery,actions,cancelled.Token).GetAwaiter().GetResult();}catch(Exception error){failure=error;}
            Check((failure is null)==(scenario=="success"),"Native outcome "+scenario);
            var retained=scenario is "native_unknown" or "restore" or "verify_restored" or "tampered_definition";
            Check(Directory.Exists(recovery)==retained,"Native evidence cleanup "+scenario);
            if(scenario=="native_unknown")Check(restoreCount==0&&unknown is {HasExited:false},"No conflicting recovery while actual process remains alive");
            if(scenario=="cancel")Check(failure is OperationCanceledException && restoreCount==1,"Cancellation survives actual native restoration");
            if(retained)
            {
                foreach(var filename in new[]{"task.xml","state.json","transition.json"}){Check(File.Exists(Path.Combine(recovery,filename)),"Retained native recovery file "+filename);File.Copy(Path.Combine(recovery,filename),Path.Combine(output,scenario+"-"+filename),true);}
            }
            if(!retained && scenario!="success")
            {
                dynamic restored=folder.GetTask(taskName);
                Check((bool)restored.Enabled==false && Convert.ToInt32(restored.Definition.Principal.RunLevel)==0 && (string)restored.Definition.Actions.Item(1).Arguments=="/d /c exit 0","Actual task policy and action restored");
            }
            Evidence.Add(new{scenario,restored=restoreCount,retained,expected_failure=failure is not null,original_task_bytes=oldXml.Length,process_exit_unknown=scenario=="native_unknown"});
        }
        finally
        {
            if(unknown is not null){if(!unknown.HasExited){unknown.Kill(true);unknown.WaitForExit(10000);}unknown.Dispose();}
            try{dynamic task=folder.GetTask(taskName);task.Stop(0);}catch(COMException){}
            folder.DeleteTask(taskName,0);
            var absent=false;try{folder.GetTask(taskName);}catch(COMException){absent=true;}Check(absent,"Only isolated task removed");
        }
    }
}
