using System.ComponentModel;
using System.IO;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed class ExecutionPayloadView(string kind) : INotifyPropertyChanged
{
    public const int PageBytes = 32768;
    private readonly List<long> _previous = [];
    private bool _loaded;
    private string _readError = "", _storageReason = "";
    public event PropertyChangedEventHandler? PropertyChanged;
    public string State { get; private set; } = "unknown";
    public string Reference { get; private set; } = "";
    public string Reason => _readError.Length > 0 ? _readError : _storageReason;
    public string Text { get; private set; } = "";
    public long TotalBytes { get; private set; }
    public long Lines { get; private set; }
    public long Offset { get; private set; }
    public long NextOffset { get; private set; }
    public bool HasMore { get; private set; }
    public bool HasPrevious => _previous.Count > 0;
    public bool NeedsLoad => Reference.Length > 0 && !_loaded;
    public bool Busy { get; private set; }
    public bool CanReadNext => HasMore && !Busy;
    public bool CanReadPrevious => HasPrevious && !Busy;
	public bool HasReadError => _readError.Length > 0 && Reference.Length > 0;
    public string Position => StateLabel + " · " + (Reference.Length == 0 ? Reason : _loaded ? $"{Offset:N0}–{NextOffset:N0} / {TotalBytes:N0} 字节 · {Lines:N0} 行" : $"预览 / {TotalBytes:N0} 字节 · {Lines:N0} 行");
    public string StateLabel => State switch
    {
        "pending" => kind == "调用" ? "正在保存调用参数" : "等待输出",
        "streaming" => "流式输出", "complete" => "已保存", "partial" => "部分输出",
        "not_stored" => "未保存", "internal" => "内部调用", _ => "旧记录"
    };

    public void Describe(JsonElement descriptor, string parentCallId = "")
    {
        var present = descriptor.ValueKind == JsonValueKind.Object;
        var reference = descriptor.Text("ref");
        var reset = Reference != reference;
        State = present ? descriptor.Text("state", "unknown") : "unknown";
        _storageReason = descriptor.Text("reason");
        TotalBytes = descriptor.Number("bytes"); Lines = descriptor.Number("lines");
        Reference = reference;
        if (reset) { _readError = ""; _loaded = false; _previous.Clear(); Offset = NextOffset = 0; HasMore = false; }
        if (!_loaded)
        {
            Text = descriptor.Text("preview");
            if (Text.Length == 0 && Reference.Length == 0)
                Text = Reason.Length > 0 ? Reason : !present || State == "unknown" ? $"旧记录未保存{kind}。" : StateLabel + "。";
        }
        if (!present && parentCallId.Length > 0) { State = "internal"; Text = "此内部调用关联根调用 " + parentCallId + "。已保存的业务请求与输出请在根调用中查看。"; }
        Notify();
    }

    public long RequestedOffset(bool previous)
    {
        if (previous) return _previous.Count > 0 ? _previous[^1] : 0;
        return _loaded && HasMore ? NextOffset : 0;
    }
    public void SetBusy(bool busy) { Busy = busy; Notify(); }
    public bool ApplyPage(JsonElement page, string expectedReference, bool previous)
    {
        if (Reference != expectedReference || page.Field("payload").Text("ref") != expectedReference) return false;
        var offset = page.Number("offset");
        var next = page.Number("next_offset");
        if (offset < 0 || next < offset || next > TotalBytes || next - offset > PageBytes) throw new InvalidDataException("工具输出分页边界无效。");
        if (page.Flag("has_more") && next <= offset) throw new InvalidDataException("工具输出分页没有推进。");
        if (_loaded && offset != Offset)
        {
            if (previous && _previous.Count > 0) _previous.RemoveAt(_previous.Count - 1);
            else if (!previous) _previous.Add(Offset);
        }
        Offset = offset; NextOffset = next; HasMore = page.Flag("has_more");
        Text = page.Text("text"); _loaded = true; _readError = "";
        Notify(); return true;
    }
    public void ReadFailed(string reason)
    {
        _readError = reason;
        if (!_loaded && Text.Length == 0) Text = reason;
        Notify();
    }
    private void Notify() => PropertyChanged?.Invoke(this, new(null));
}
