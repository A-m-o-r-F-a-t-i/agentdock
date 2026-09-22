using System.Windows.Controls;

namespace AgentDock.ControlPanel;

// Programmatically created cards use the same live resource expressions as XAML.
// These helpers never cache a brush or introduce an independent palette.
internal sealed class ThemeTextBlock : TextBlock
{
    public string ForegroundResource { set => SetResourceReference(ForegroundProperty, value); }
}

internal sealed class ThemeBorder : Border
{
    public string BackgroundResource { set => SetResourceReference(BackgroundProperty, value); }
    public string BorderResource { set => SetResourceReference(BorderBrushProperty, value); }
}
