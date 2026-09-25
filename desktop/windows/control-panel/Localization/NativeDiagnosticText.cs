namespace AgentDock.ControlPanel;
internal static class NativeDiagnosticText
{
    internal static string Describe(string original)
    {
        var separator = original.IndexOf(':');
        var code = separator is > 0 and < 80 ? original[..separator].Trim() : "";
        var key = code switch {
            "task_owner_mismatch" => "NativeTaskOwnerMismatch",
            "task_stop_incomplete" => "NativeTaskStopIncomplete",
            "task_backup_invalid" => "NativeTaskBackupInvalid",
            "elevated_unavailable" => "NativeElevatedUnavailable",
            _ => "NativeOperationFailed"
        };
        return UiText.Format("NativeDiagnosticOriginal", UiText.Get(key), original);
    }
}
