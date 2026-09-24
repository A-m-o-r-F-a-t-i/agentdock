using System.Windows.Controls;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    internal void NavigatePage(string page)
    {
        var target = page == "display" ? "runtime" : page;
        var item = MainPages.Items.OfType<TabItem>().FirstOrDefault(tab => tab.Tag as string == target);
        if (item is null) return;
        MainPages.SelectedItem = item;
        if (page == "display") DisplayAndThemeGroup.BringIntoView();
    }
}
