using System.Buffers;
using System.Globalization;
using System.Text;
using System.Text.Json.Serialization;

namespace AgentDock.ControlPanel;

public sealed record ToolOutputSettings(
    [property: JsonPropertyName("enabled")] bool Enabled,
    [property: JsonPropertyName("max_chars")] int MaxChars)
{
    public const int Minimum = 1000, Maximum = 100000;
    public static ToolOutputSettings Default { get; } = new(true, 20000);
    public bool Valid => MaxChars is >= Minimum and <= Maximum;
    public static bool TryParse(string text, bool enabled, out ToolOutputSettings settings)
    {
        settings = Default;
        if (!int.TryParse(text.Trim(), NumberStyles.None, CultureInfo.InvariantCulture, out var value) || value is < Minimum or > Maximum) return false;
        settings = new(enabled, value); return true;
    }
    public PayloadPageBudget Budget => !Valid ? throw new ArgumentOutOfRangeException(nameof(MaxChars)) :
        Enabled ? new(MaxChars, checked(4 * MaxChars)) : PayloadPageBudget.BytesOnly;

    public static int ScalarCount(string text)
    {
        var count = 0; var remaining = text.AsSpan();
        while (!remaining.IsEmpty)
        {
            if (Rune.DecodeFromUtf16(remaining, out _, out var consumed) != OperationStatus.Done)
                throw new System.IO.InvalidDataException("输出包含损坏的 Unicode 字符。");
            remaining = remaining[consumed..]; count++;
        }
        return count;
    }
}

public readonly record struct PayloadPageBudget(int LimitChars, int MaxBytes)
{
    public static PayloadPageBudget BytesOnly => new(0, 32768);
    public string Query => LimitChars > 0 ? "limit_chars=" + LimitChars.ToString(CultureInfo.InvariantCulture) : "limit=" + MaxBytes.ToString(CultureInfo.InvariantCulture);
}
