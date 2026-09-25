using System.ComponentModel;
using System.IO;
using System.Text.Json;
using System.Text;

namespace AgentDock.ControlPanel;

public sealed class ExecutionPayloadView(string kind) : INotifyPropertyChanged
{
    public const int PageBytes = 32768;
    private readonly List<long> _previous = [];
    private bool _loaded;
    private bool _knownBytes;
    private int _displayedChars;
    private PayloadPageBudget? _loadedBudget;
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
    public string Position
    {
        get
        {
            if (State == "unknown") return "该记录未保存输出正文";
            if (Reference.Length == 0) return Reason.Length > 0 ? StateLabel + "：" + Reason : StateLabel;
            if (!_knownBytes) return $"当前显示 {_displayedChars:N0} 字符，总长度暂未知";
            if (TotalBytes == 0) return State == "streaming" ? "当前已保存 0 字节，输出继续写入" : "无输出，0 字节";
            if (!_loaded) return State == "partial" ? $"当前为预览，已保存部分输出共 {TotalBytes:N0} 字节" : $"当前为预览，已保存内容共 {TotalBytes:N0} 字节";
            if (State == "complete" && Offset == 0 && NextOffset == TotalBytes && !HasMore) return $"已显示全部内容，共 {TotalBytes:N0} 字节";
            var range = NextOffset > Offset ? $"显示第 {Offset + 1:N0}–{NextOffset:N0} 字节" : "当前页无内容";
            return State switch
            {
                "streaming" => $"{range}，当前已保存 {TotalBytes:N0} 字节，输出继续写入",
                "partial" => $"{range}，已保存部分输出共 {TotalBytes:N0} 字节" + (_storageReason.Length > 0 ? "；" + _storageReason : ""),
                _ => $"{range}，共 {TotalBytes:N0} 字节"
            };
        }
    }
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
        _knownBytes = descriptor.OptionalNumber("bytes") is >= 0;
        TotalBytes = descriptor.Number("bytes"); Lines = descriptor.Number("lines");
        Reference = reference;
        if (reset) { _readError = ""; _loaded = false; _loadedBudget = null; _previous.Clear(); Offset = NextOffset = 0; HasMore = false; }
        if (!_loaded)
        {
            Text = descriptor.Text("preview");
            _displayedChars = ToolOutputSettings.ScalarCount(Text);
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
    public bool NeedsBudgetReload(PayloadPageBudget budget) => _loaded && _loadedBudget != budget;
    public bool ApplyPage(JsonElement page, string expectedReference, bool previous, PayloadPageBudget? requestedBudget = null)
    {
        if (Reference != expectedReference || page.Field("payload").Text("ref") != expectedReference) return false;
        var budget = requestedBudget ?? PayloadPageBudget.BytesOnly;
        var offset = page.Number("offset");
        var next = page.Number("next_offset");
        var text = page.Text("text");
        var scalars = ToolOutputSettings.ScalarCount(text);
        if (offset < 0 || next < offset || _knownBytes && next > TotalBytes || next - offset > budget.MaxBytes || Encoding.UTF8.GetByteCount(text) != next - offset)
            throw new InvalidDataException("工具输出分页边界无效。");
        if (budget.LimitChars > 0 && (scalars > budget.LimitChars || page.Text("unit") != "unicode_scalar" || page.Number("limit_chars") != budget.LimitChars || page.Number("returned_chars") != scalars))
            throw new InvalidDataException("工具输出字符预算或计数不一致。");
        if (page.Flag("has_more") && next <= offset) throw new InvalidDataException("工具输出分页没有推进。");
        if (NeedsBudgetReload(budget))
        {
            if (offset != 0) throw new InvalidDataException("修改字符上限后必须从已知起点重新分页。");
            _previous.Clear(); _loaded = false;
        }
        if (_loaded && offset != Offset)
        {
            if (previous && _previous.Count > 0) _previous.RemoveAt(_previous.Count - 1);
            else if (!previous) _previous.Add(Offset);
        }
        Offset = offset; NextOffset = next; HasMore = page.Flag("has_more");
        Text = text; _displayedChars = scalars; _loaded = true; _loadedBudget = budget; _readError = "";
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
