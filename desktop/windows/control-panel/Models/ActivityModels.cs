using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Globalization;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed class ActivityTaskList
{
    public List<ActivityTask> Tasks { get; set; } = [];
    public int Count { get; set; }
}

public sealed class ActivityTask
{
    public string Id { get; set; } = "";
    public string Title { get; set; } = "";
    public string Goal { get; set; } = "";
    public string Project { get; set; } = "";
    public string Status { get; set; } = "";
    public string Outcome { get; set; } = "";
    public string WorkspaceId { get; set; } = "";
    public string ActiveThreadId { get; set; } = "main";
    public ActivityThread? ActiveThread { get; set; }
    public int CompletedStepCount { get; set; }
    public int StepCount { get; set; }
    public DateTimeOffset UpdatedAt { get; set; }
    public DateTimeOffset? ArchivedAt { get; set; }
    public List<ActivityStep> Steps { get; set; } = [];
    public List<ActivityCondition> Conditions { get; set; } = [];
    public ActivityReview? FinalReview { get; set; }
    public string StateLabel => ActivityText.State(ArchivedAt is null ? (Outcome == "cancelled" ? Outcome : Status) : "archived");
    public string Detail => Id.Length == 0 ? ActivityText.Get("AllActivityDetail") : $"{StateLabel}  ·  {CompletedStepCount}/{StepCount}  ·  {UpdatedAt.ToLocalTime():MM-dd HH:mm}";
    public string SearchText => $"{Title} {Id} {Project} {WorkspaceId}";
}

public sealed class ActivityTaskDetail { public ActivityTask Task { get; set; } = new(); }
public sealed class ActivityThreadList { public List<ActivityThread> Threads { get; set; } = []; }

public sealed class ActivityThread
{
    public string Id { get; set; } = "";
    public string ThreadId { get; set; } = "";
    public string TaskId { get; set; } = "";
    public string Title { get; set; } = "";
    public string Status { get; set; } = "";
    public string WorkspaceId { get; set; } = "";
    public string CurrentStepId { get; set; } = "";
    public string Summary { get; set; } = "";
    public string NextAction { get; set; } = "";
    public string BlockReason { get; set; } = "";
    public string ParentThreadId { get; set; } = "";
    public string CheckpointEventId { get; set; } = "";
    public List<ActivityStep> Steps { get; set; } = [];
    public string EffectiveId => Id.Length > 0 ? Id : ThreadId;
    public string Display => $"{Title}  ·  {ActivityText.State(Status)}";
}

public sealed class ActivityStep
{
    public string Id { get; set; } = "";
    public string Title { get; set; } = "";
    public string Status { get; set; } = "";
}
public sealed class ActivityCondition { public string Id { get; set; } = ""; public string Text { get; set; } = ""; }
public sealed class ActivityReview
{
    public string Status { get; set; } = "";
    public string Summary { get; set; } = "";
    public List<string> VerifiedFacts { get; set; } = [];
    public List<string> OpenRisks { get; set; } = [];
    public List<string> MissingChecks { get; set; } = [];
}

public sealed class ActivityEvent
{
    public int SchemaVersion { get; set; }
    public ulong Seq { get; set; }
    public string EventId { get; set; } = "";
    public DateTimeOffset CreatedAt { get; set; }
    public string TaskId { get; set; } = "";
    public string ThreadId { get; set; } = "";
    public string StepId { get; set; } = "";
    public string WorkspaceId { get; set; } = "";
    public string ActivityLabel { get; set; } = "";
    public string Kind { get; set; } = "";
    public string Status { get; set; } = "";
    public string Title { get; set; } = "";
    public string ToolName { get; set; } = "";
    public string SessionId { get; set; } = "";
    public string Runtime { get; set; } = "";
    public string Workdir { get; set; } = "";
    public string DisplayCommand { get; set; } = "";
    public int? ExitCode { get; set; }
    public bool? CommandOk { get; set; }
    public bool TimedOut { get; set; }
    public long ElapsedMs { get; set; }
    public string OutputPreview { get; set; } = "";
    public string StderrPreview { get; set; } = "";
    public bool StdoutTruncated { get; set; }
    public bool StderrTruncated { get; set; }
    public string LogicalPath { get; set; } = "";
    public string ResolvedPath { get; set; } = "";
    public int Insertions { get; set; }
    public int Deletions { get; set; }
    public bool ChangeStatsKnown { get; set; }
    public string Summary { get; set; } = "";
}

public sealed record ActivityStreamMessage(string Type, ulong Sequence, ActivityEvent? Event = null, string Message = "");

public sealed class ActivityRow : INotifyPropertyChanged
{
    public const int MaxOutputCharacters = 16 * 1024;
    public ActivityEvent Latest { get; private set; }
    public string Stdout { get; private set; } = "";
    public string Stderr { get; private set; } = "";
    public bool Truncated { get; private set; }
    public bool IsExpanded { get; set; }
    public string Command { get; private set; } = "";
    public string Workdir { get; private set; } = "";
    public string FilePath { get; private set; } = "";
    public string Runtime { get; private set; } = "";
    public string Title { get; private set; } = "";
    public string Summary { get; private set; } = "";
    public string SessionId => Latest.SessionId;
    public bool IsCommand => SessionId.Length > 0 && Latest.Kind.StartsWith("command.", StringComparison.Ordinal);
    public bool CanStop => IsCommand && Latest.Kind != "command.completed";
    public string State => ActivityText.State(Latest.Status);
    public string Heading => Title.Length > 0 ? Title : ActivityText.Kind(Latest.Kind);
    public string Metadata => string.Join("  ·  ", new[]
    {
        Latest.CreatedAt.ToLocalTime().ToString("HH:mm:ss", CultureInfo.CurrentCulture), State,
        Latest.TaskId, Latest.ThreadId, Latest.StepId, Runtime,
        Latest.ExitCode is int code ? $"exit {code}" : "",
        IsCommand ? $"{Latest.ElapsedMs / 1000d:0.00} s" : ""
    }.Where(value => value.Length > 0));
    public string Details => string.Join(Environment.NewLine, new[]
    {
        Command, Workdir, FilePath.Length > 0 ? FilePath + (Latest.ChangeStatsKnown ? $"  (+{Latest.Insertions} -{Latest.Deletions})" : "") : "", Summary
    }.Where(value => value.Length > 0));
    public string Output => Stdout + (Stderr.Length == 0 ? "" : Environment.NewLine + "[stderr]" + Environment.NewLine + Stderr);
    public bool HasOutput => Output.Length > 0 || Truncated;
    public string OutputLabel => Truncated ? ActivityText.Get("OutputTruncated") : ActivityText.Get("Output");
    public event PropertyChangedEventHandler? PropertyChanged;

    public ActivityRow(ActivityEvent first) { Latest = first; Apply(first); }

    public void Apply(ActivityEvent value)
    {
        Latest = value;
        if (value.DisplayCommand.Length > 0) Command = value.DisplayCommand;
        if (value.Workdir.Length > 0) Workdir = value.Workdir;
        if (value.ResolvedPath.Length > 0) FilePath = value.ResolvedPath;
        if (value.Runtime.Length > 0) Runtime = value.Runtime;
        if (value.Title.Length > 0) Title = value.Title;
        if (value.Summary.Length > 0) Summary = value.Summary;
        Stdout = AppendTail(Stdout, value.OutputPreview);
        Stderr = AppendTail(Stderr, value.StderrPreview);
        Truncated |= value.StdoutTruncated || value.StderrTruncated;
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(null));
    }

    private string AppendTail(string previous, string next)
    {
        if (next.Length == 0) return previous;
        var joined = previous + next;
        if (joined.Length <= MaxOutputCharacters) return joined;
        Truncated = true;
        var offset = joined.Length - MaxOutputCharacters;
        if (char.IsLowSurrogate(joined[offset])) offset++;
        return joined[offset..];
    }
}

// Called only on the UI thread. Both the visible rows and session index are bounded.
public sealed class ActivityTimeline
{
    public const int MaxRows = 1000;
    private readonly Dictionary<string, ActivityRow> _commands = new(StringComparer.Ordinal);
    public ObservableCollection<ActivityRow> Rows { get; } = [];
    public ulong LastSequence { get; private set; }
    public int RemovedRowCount { get; private set; }

    public bool Apply(ActivityEvent value)
    {
        if (value.SchemaVersion != 1 || value.Seq <= LastSequence) return false;
        LastSequence = value.Seq;
        var key = $"{value.TaskId}/{value.ThreadId}/{value.SessionId}";
        var command = value.SessionId.Length > 0 && value.Kind.StartsWith("command.", StringComparison.Ordinal);
        if (command && _commands.TryGetValue(key, out var row)) row.Apply(value);
        else
        {
            row = new ActivityRow(value);
            Rows.Add(row);
            if (command) _commands[key] = row;
        }
        while (Rows.Count > MaxRows)
        {
            var old = Rows[0];
            if (old.IsCommand) _commands.Remove($"{old.Latest.TaskId}/{old.Latest.ThreadId}/{old.SessionId}");
            Rows.RemoveAt(0);
            RemovedRowCount++;
        }
        return true;
    }

    public void Advance(ulong sequence) => LastSequence = Math.Max(LastSequence, sequence);
    public void Reset(ulong sequence = 0)
    {
        Rows.Clear(); _commands.Clear(); LastSequence = sequence; RemovedRowCount = 0;
    }
}
