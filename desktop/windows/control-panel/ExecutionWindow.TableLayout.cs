using System;
using System.Windows;
using System.Windows.Controls;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    // Fixed diagnostic columns must not squeeze the title or edit counts to
    // zero. Header and rows share horizontal movement; the inner virtualized
    // list retains a finite-height vertical viewport.
    private const double DetailedTableMinimumWidth = 940;

    private void CallTable_SizeChanged(object sender, SizeChangedEventArgs e) => UpdateCallTableWidth();

    private void UpdateCallTableWidth()
    {
        if (CallTable is null || CallTableScroller is null) return;
        var detailed = _preferences.DetailedCalls;
        CallTableScroller.HorizontalScrollBarVisibility = detailed ? ScrollBarVisibility.Auto : ScrollBarVisibility.Disabled;
        CallTable.Width = Math.Max(CallTableScroller.ActualWidth, detailed ? DetailedTableMinimumWidth : 0);
        if (!detailed && CallTableScroller.HorizontalOffset != 0) CallTableScroller.ScrollToHorizontalOffset(0);
    }
}
