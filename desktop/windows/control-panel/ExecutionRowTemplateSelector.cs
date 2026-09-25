using System.Windows;
using System.Windows.Controls;

namespace AgentDock.ControlPanel;

public sealed class ExecutionRowTemplateSelector : DataTemplateSelector
{
    public DataTemplate? CallTemplate { get; set; }
    public DataTemplate? MessageTemplate { get; set; }
    public override DataTemplate? SelectTemplate(object item, DependencyObject container) => item is ExecutionCallRow { IsInsertion: true } ? MessageTemplate : CallTemplate;
}
