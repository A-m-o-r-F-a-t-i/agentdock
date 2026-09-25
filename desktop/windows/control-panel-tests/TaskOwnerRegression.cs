using System.Security;
using AgentDock.ControlPanel;
internal static class TaskOwnerRegression
{
 internal static void Run()
 {
   var root=Path.Combine(Path.GetTempPath(),"AgentDock task-policy fixture");var sid="S-1-5-21-1-2-3-1001";
   var command=Path.Combine(root,"bin","agentdock-tray.exe");
   var xml=$"<Task xmlns=\"http://schemas.microsoft.com/windows/2004/02/mit/task\"><Principals><Principal><UserId>{sid}</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Actions><Exec><Command>{SecurityElement.Escape(command)}</Command><Arguments>--run-core-task --runtime-root &quot;{SecurityElement.Escape(root)}&quot;</Arguments></Exec></Actions></Task>";
   TaskDefinitionPolicy.Validate(xml,"AgentDock",root,sid,value=>value);var assertions=1;
   foreach(var invalid in new[]{xml.Replace(sid,"S-1-5-18"),xml.Replace("InteractiveToken","Password"),xml.Replace("HighestAvailable","LeastPrivilege"),xml.Replace(command,Path.Combine(root,"not-agentdock.exe")),xml.Replace("--run-core-task","--background"),xml.Replace("</Arguments>"," --extra</Arguments>"),xml.Replace("</Actions>","<Exec><Command>other.exe</Command></Exec></Actions>"),xml.Replace("<Actions>","<Actions><ComHandler/>"),"<!DOCTYPE Task [<!ENTITY x SYSTEM 'file:///never'>]>"+xml}) {
     try {TaskDefinitionPolicy.Validate(invalid,"AgentDock",root,sid,value=>value);throw new Exception("foreign or malformed definition accepted");}
     catch(Exception ex) when(ex is InvalidOperationException or System.Xml.XmlException) {assertions++;}
   }
   foreach(var otherRoot in new[]{Path.Combine(root,"nested"),"relative"}) {
     try{TaskDefinitionPolicy.Validate(xml,"AgentDock",otherRoot,sid,value=>value);throw new Exception("other root accepted");}catch(InvalidOperationException){assertions++;}
   }
   Console.WriteLine($"Task definition ownership: {assertions} assertions passed; no task, elevation, service or UI operation executed.");
 }
}
