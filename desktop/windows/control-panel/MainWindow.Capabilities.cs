using System.IO;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using Microsoft.Win32;
using Button = System.Windows.Controls.Button;
using CheckBox = System.Windows.Controls.CheckBox;
using HorizontalAlignment = System.Windows.HorizontalAlignment;
using MessageBox = System.Windows.MessageBox;
using Orientation = System.Windows.Controls.Orientation;
using TextBox = System.Windows.Controls.TextBox;
using Brushes = System.Windows.Media.Brushes;
using Color = System.Windows.Media.Color;
using Forms = System.Windows.Forms;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private readonly SemaphoreSlim _capabilityGate = new(1, 1);
    private CapabilityInventory _capabilityInventory = new();
    private bool _updatingCapabilities;

    private async Task RefreshCapabilitiesAsync(bool coreAvailable = true, bool showErrors = true)
    {
        if (!await _capabilityGate.WaitAsync(0))
        {
            return;
        }
        try
        {
            if (!coreAvailable)
            {
                _capabilityInventory = new CapabilityInventory();
                RenderCapabilityInventory();
                CapabilityStatusText.Text = UiText.Get("CapabilitiesRequireRunningCore");
                return;
            }
            await LoadCapabilityInventoryCoreAsync();
        }
        catch (Exception ex)
        {
            CapabilityStatusText.Text = ex.Message;
            if (showErrors)
            {
                MessageBox.Show(this, ex.Message, "AgentDock", MessageBoxButton.OK, MessageBoxImage.Error);
            }
        }
        finally
        {
            _capabilityGate.Release();
        }
    }

    private async Task LoadCapabilityInventoryCoreAsync()
    {
        CapabilityStatusText.Text = UiText.Get("LoadingCapabilities");
        _capabilityInventory = await _runtime.GetCapabilityInventoryAsync();
        _capabilityInventory.Plugins ??= [];
        _capabilityInventory.Skills ??= [];
        _capabilityInventory.McpServers ??= [];
        RenderCapabilityInventory();
        CapabilityStatusText.Text = UiText.Format(
            "CapabilitiesLoaded",
            _capabilityInventory.Plugins.Count,
            _capabilityInventory.Skills.Count,
            _capabilityInventory.McpServers.Count);
    }

    private async Task ExecuteCapabilityActionAsync(string pendingText, Func<Task> action)
    {
        if (!await _capabilityGate.WaitAsync(0))
        {
            return;
        }
        try
        {
            CapabilityStatusText.Text = pendingText;
            await action();
            await LoadCapabilityInventoryCoreAsync();
        }
        catch (Exception ex)
        {
            CapabilityStatusText.Text = ex.Message;
            MessageBox.Show(this, ex.Message, "AgentDock", MessageBoxButton.OK, MessageBoxImage.Error);
            RenderCapabilityInventory();
        }
        finally
        {
            _capabilityGate.Release();
        }
    }

    private void RenderCapabilityInventory()
    {
        var previous = _updatingCapabilities;
        _updatingCapabilities = true;
        try
        {
            PluginListPanel.Children.Clear();
            StandaloneSkillListPanel.Children.Clear();
            StandaloneMcpListPanel.Children.Clear();

            var plugins = _capabilityInventory.Plugins
                .OrderBy(plugin => plugin.Name, StringComparer.OrdinalIgnoreCase)
                .ToList();
            foreach (var plugin in plugins)
            {
                PluginListPanel.Children.Add(BuildPluginCard(plugin));
            }
            if (plugins.Count == 0)
            {
                PluginListPanel.Children.Add(BuildEmptyCapabilityText("NoPlugins"));
            }

            var ownedSkills = plugins.SelectMany(plugin => plugin.Skills ?? []).ToHashSet(StringComparer.Ordinal);
            var ownedMcpServers = plugins.SelectMany(plugin => plugin.McpServers ?? []).ToHashSet(StringComparer.Ordinal);

            var standaloneSkills = _capabilityInventory.Skills
                .Where(skill => string.IsNullOrWhiteSpace(skill.Plugin) && !ownedSkills.Contains(skill.Identifier))
                .OrderBy(skill => skill.DisplayName, StringComparer.CurrentCultureIgnoreCase)
                .ToList();
            foreach (var skill in standaloneSkills)
            {
                StandaloneSkillListPanel.Children.Add(BuildSkillCapabilityRow(skill, nested: false, pluginName: ""));
            }
            if (standaloneSkills.Count == 0)
            {
                StandaloneSkillListPanel.Children.Add(BuildEmptyCapabilityText("NoStandaloneSkills"));
            }

            var standaloneMcp = _capabilityInventory.McpServers
                .Where(server => string.IsNullOrWhiteSpace(server.Plugin) && !ownedMcpServers.Contains(server.Name))
                .OrderBy(server => server.Name, StringComparer.OrdinalIgnoreCase)
                .ToList();
            foreach (var server in standaloneMcp)
            {
                StandaloneMcpListPanel.Children.Add(BuildMcpCapabilityRow(server, nested: false, pluginName: ""));
            }
            if (standaloneMcp.Count == 0)
            {
                StandaloneMcpListPanel.Children.Add(BuildEmptyCapabilityText("NoStandaloneMcpServers"));
            }
        }
        finally
        {
            _updatingCapabilities = previous;
        }
    }

    private Border BuildPluginCard(PluginCapabilityInfo plugin)
    {
        var content = new StackPanel();
        var header = new Grid();
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

        var title = new TextBlock
        {
            Text = plugin.Name,
            FontSize = 15,
            FontWeight = FontWeights.SemiBold,
            VerticalAlignment = VerticalAlignment.Center
        };
        var update = new Button
        {
            Content = UiText.Get("UpdatePlugin"),
            Tag = plugin.Name,
            MinWidth = 70,
            Margin = new Thickness(8, 0, 0, 0)
        };
        update.Click += PluginUpdateButton_Click;
        var remove = new Button
        {
            Content = UiText.Get("Delete"),
            Tag = plugin.Name,
            MinWidth = 70,
            Margin = new Thickness(8, 0, 0, 0)
        };
        remove.Click += PluginRemoveButton_Click;
        var toggle = new CheckBox
        {
            Content = UiText.Get("Enabled"),
            IsChecked = plugin.Enabled,
            Tag = plugin.Name,
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(14, 0, 0, 0)
        };
        toggle.Checked += PluginToggle_Changed;
        toggle.Unchecked += PluginToggle_Changed;

        Grid.SetColumn(title, 0);
        Grid.SetColumn(update, 1);
        Grid.SetColumn(remove, 2);
        Grid.SetColumn(toggle, 3);
        header.Children.Add(title);
        header.Children.Add(update);
        header.Children.Add(remove);
        header.Children.Add(toggle);
        content.Children.Add(header);
        content.Children.Add(new TextBlock
        {
            Text = plugin.Description,
            Foreground = new SolidColorBrush(Color.FromRgb(102, 112, 133)),
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 6, 0, 3)
        });
        content.Children.Add(new TextBlock
        {
            Text = UiText.Format("PluginPackageMetadata", plugin.Version, plugin.Path),
            Foreground = new SolidColorBrush(Color.FromRgb(102, 112, 133)),
            TextWrapping = TextWrapping.Wrap,
            Margin = new Thickness(0, 0, 0, 10)
        });

        var skillsByName = _capabilityInventory.Skills
            .GroupBy(skill => skill.Identifier, StringComparer.Ordinal)
            .ToDictionary(group => group.Key, group => group.First(), StringComparer.Ordinal);
        var mcpByName = _capabilityInventory.McpServers
            .GroupBy(server => server.Name, StringComparer.Ordinal)
            .ToDictionary(group => group.Key, group => group.First(), StringComparer.Ordinal);
        if ((plugin.Skills?.Count ?? 0) > 0)
        {
            content.Children.Add(BuildPluginSectionTitle(UiText.Get("PluginSkills")));
            foreach (var name in (plugin.Skills ?? []).OrderBy(value => value, StringComparer.OrdinalIgnoreCase))
            {
                content.Children.Add(skillsByName.TryGetValue(name, out var skill)
                    ? BuildSkillCapabilityRow(skill, nested: true, pluginName: plugin.Name)
                    : BuildUnavailableCapabilityRow("Skill", name));
            }
        }
        if ((plugin.McpServers?.Count ?? 0) > 0)
        {
            content.Children.Add(BuildPluginSectionTitle(UiText.Get("PluginMcpServers")));
            foreach (var name in (plugin.McpServers ?? []).OrderBy(value => value, StringComparer.OrdinalIgnoreCase))
            {
                content.Children.Add(mcpByName.TryGetValue(name, out var server)
                    ? BuildMcpCapabilityRow(server, nested: true, pluginName: plugin.Name)
                    : BuildUnavailableCapabilityRow("MCP", name));
            }
        }

        return new Border
        {
            Child = content,
            BorderBrush = new SolidColorBrush(Color.FromRgb(208, 213, 221)),
            BorderThickness = new Thickness(1),
            CornerRadius = new CornerRadius(6),
            Padding = new Thickness(14),
            Margin = new Thickness(0, 0, 0, 10),
            Background = Brushes.White
        };
    }

    private static TextBlock BuildPluginSectionTitle(string text) => new()
    {
        Text = text,
        FontWeight = FontWeights.SemiBold,
        Margin = new Thickness(0, 8, 0, 3)
    };

    private Border BuildSkillCapabilityRow(SkillCapabilityInfo skill, bool nested, string pluginName)
    {
        var details = skill.Description;
        var metadata = new List<string>();
        if (!string.IsNullOrWhiteSpace(skill.ActiveVersion))
        {
            metadata.Add(skill.ActiveVersion);
        }
        if (skill.Bundled)
        {
            metadata.Add(UiText.Get("Bundled"));
        }
        if (metadata.Count > 0)
        {
            details = string.IsNullOrWhiteSpace(details)
                ? string.Join(" · ", metadata)
                : details + " · " + string.Join(" · ", metadata);
        }
        var title = skill.DisplayName;
        if (!string.Equals(skill.DisplayName, skill.Identifier, StringComparison.Ordinal))
        {
            title += $" ({skill.Identifier})";
        }
        return BuildCapabilityToggleRow(
            title,
            details,
            skill.Enabled,
            new CapabilityToggleTarget("skill", skill.Identifier, pluginName),
            nested);
    }

    private Border BuildMcpCapabilityRow(McpCapabilityInfo server, bool nested, string pluginName)
    {
        var metadata = UiText.Format("McpStatusSummary", server.Status, server.ToolCount);
        var details = string.IsNullOrWhiteSpace(server.Description)
            ? metadata
            : server.Description + " · " + metadata;
        if (!string.IsNullOrWhiteSpace(server.LastErrorCode))
        {
            details += " · " + server.LastErrorCode;
        }
        return BuildCapabilityToggleRow(
            server.Name,
            details,
            server.Enabled,
            new CapabilityToggleTarget("mcp", server.Name, pluginName),
            nested);
    }

    private Border BuildCapabilityToggleRow(
        string title,
        string description,
        bool enabled,
        CapabilityToggleTarget target,
        bool nested)
    {
        var row = new Grid();
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var text = new StackPanel();
        text.Children.Add(new TextBlock
        {
            Text = title,
            FontWeight = FontWeights.Medium,
            TextWrapping = TextWrapping.Wrap
        });
        if (!string.IsNullOrWhiteSpace(description))
        {
            text.Children.Add(new TextBlock
            {
                Text = description,
                Foreground = new SolidColorBrush(Color.FromRgb(102, 112, 133)),
                TextWrapping = TextWrapping.Wrap,
                Margin = new Thickness(0, 2, 14, 0)
            });
        }
        var toggle = new CheckBox
        {
            IsChecked = enabled,
            Tag = target,
            VerticalAlignment = VerticalAlignment.Center,
            ToolTip = enabled ? UiText.Get("DisableCapability") : UiText.Get("EnableCapability")
        };
        toggle.Checked += CapabilityToggle_Changed;
        toggle.Unchecked += CapabilityToggle_Changed;
        Grid.SetColumn(text, 0);
        Grid.SetColumn(toggle, 1);
        row.Children.Add(text);
        row.Children.Add(toggle);
        return new Border
        {
            Child = row,
            BorderBrush = new SolidColorBrush(Color.FromRgb(234, 236, 240)),
            BorderThickness = new Thickness(0, 0, 0, 1),
            Padding = nested ? new Thickness(18, 7, 8, 7) : new Thickness(8, 9, 8, 9)
        };
    }

    private static Border BuildUnavailableCapabilityRow(string kind, string name) => new()
    {
        Child = new TextBlock
        {
            Text = UiText.Format("UnavailablePluginMember", kind, name),
            Foreground = new SolidColorBrush(Color.FromRgb(180, 35, 24)),
            TextWrapping = TextWrapping.Wrap
        },
        BorderBrush = new SolidColorBrush(Color.FromRgb(254, 205, 202)),
        BorderThickness = new Thickness(0, 0, 0, 1),
        Padding = new Thickness(18, 7, 8, 7)
    };

    private static TextBlock BuildEmptyCapabilityText(string resourceKey) => new()
    {
        Text = UiText.Get(resourceKey),
        Foreground = new SolidColorBrush(Color.FromRgb(102, 112, 133)),
        Margin = new Thickness(8),
        TextWrapping = TextWrapping.Wrap
    };

    private async void RefreshCapabilitiesButton_Click(object sender, RoutedEventArgs e) =>
        await RefreshCapabilitiesAsync(_snapshot?.Healthy == true);

    private async void PluginToggle_Changed(object sender, RoutedEventArgs e)
    {
        if (_updatingCapabilities || sender is not CheckBox toggle || toggle.Tag is not string name)
        {
            return;
        }
        var enabled = toggle.IsChecked == true;
        await ExecuteCapabilityActionAsync(
            UiText.Format(enabled ? "EnablingPlugin" : "DisablingPlugin", name),
            () => _runtime.SetPluginEnabledAsync(name, enabled));
    }

    private async void CapabilityToggle_Changed(object sender, RoutedEventArgs e)
    {
        if (_updatingCapabilities || sender is not CheckBox toggle || toggle.Tag is not CapabilityToggleTarget target)
        {
            return;
        }
        var enabled = toggle.IsChecked == true;
        var pendingKey = enabled ? "EnablingCapability" : "DisablingCapability";
        await ExecuteCapabilityActionAsync(
            UiText.Format(pendingKey, target.Name),
            string.IsNullOrWhiteSpace(target.Plugin)
                ? target.Kind == "skill"
                    ? () => _runtime.SetSkillEnabledAsync(target.Name, enabled)
                    : () => _runtime.SetMcpEnabledAsync(target.Name, enabled)
                : () => _runtime.SetPluginMemberEnabledAsync(
                    target.Plugin,
                    target.Kind == "skill" ? "skill" : "mcp_server",
                    target.Name,
                    enabled));
    }

    private async void AddPluginButton_Click(object sender, RoutedEventArgs e)
    {
        if (_updatingCapabilities)
        {
            return;
        }
        var source = ShowPluginSourceDialog(pluginName: null);
        if (string.IsNullOrWhiteSpace(source))
        {
            return;
        }
        await ExecuteCapabilityActionAsync(
            UiText.Format("InstallingPluginPackage", Path.GetFileName(source.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar))),
            () => _runtime.InstallPluginAsync(source));
    }

    private async void PluginUpdateButton_Click(object sender, RoutedEventArgs e)
    {
        if (_updatingCapabilities || sender is not Button button || button.Tag is not string name)
        {
            return;
        }
        var source = ShowPluginSourceDialog(name);
        if (string.IsNullOrWhiteSpace(source))
        {
            return;
        }
        await ExecuteCapabilityActionAsync(
            UiText.Format("UpdatingPluginPackage", name),
            () => _runtime.UpdatePluginAsync(name, source));
    }

    private async void PluginRemoveButton_Click(object sender, RoutedEventArgs e)
    {
        if (_updatingCapabilities || sender is not Button button || button.Tag is not string name)
        {
            return;
        }
        var confirm = MessageBox.Show(
            this,
            UiText.Format("ConfirmRemovePlugin", name),
            "AgentDock",
            MessageBoxButton.YesNo,
            MessageBoxImage.Warning);
        if (confirm != MessageBoxResult.Yes)
        {
            return;
        }
        await ExecuteCapabilityActionAsync(
            UiText.Format("RemovingPlugin", name),
            () => _runtime.RemovePluginAsync(name));
    }

    private string? ShowPluginSourceDialog(string? pluginName)
    {
        var updating = !string.IsNullOrWhiteSpace(pluginName);
        var dialog = new Window
        {
            Title = UiText.Get(updating ? "UpdatePlugin" : "AddPlugin"),
            Owner = this,
            Width = 680,
            Height = 280,
            MinWidth = 560,
            WindowStartupLocation = WindowStartupLocation.CenterOwner,
            ResizeMode = ResizeMode.NoResize,
            ShowInTaskbar = false
        };
        var pathInput = new TextBox
        {
            Margin = new Thickness(0, 6, 0, 10),
            MinHeight = 30,
            VerticalContentAlignment = VerticalAlignment.Center
        };
        var chooseFolder = new Button
        {
            Content = UiText.Get("ChoosePluginFolder"),
            MinWidth = 120,
            Height = 32
        };
        chooseFolder.Click += (_, _) =>
        {
            using var picker = new Forms.FolderBrowserDialog
            {
                Description = UiText.Get("ChoosePluginFolder"),
                UseDescriptionForTitle = true,
                ShowNewFolderButton = false
            };
            if (picker.ShowDialog() == Forms.DialogResult.OK)
            {
                pathInput.Text = picker.SelectedPath;
            }
        };
        var chooseZip = new Button
        {
            Content = UiText.Get("ChoosePluginZip"),
            MinWidth = 120,
            Height = 32,
            Margin = new Thickness(8, 0, 0, 0)
        };
        chooseZip.Click += (_, _) =>
        {
            var picker = new Microsoft.Win32.OpenFileDialog
            {
                Title = UiText.Get("ChoosePluginZip"),
                Filter = "AgentDock plugin (*.zip)|*.zip|All files (*.*)|*.*",
                CheckFileExists = true,
                Multiselect = false
            };
            if (picker.ShowDialog(dialog) == true)
            {
                pathInput.Text = picker.FileName;
            }
        };

        var accept = new Button
        {
            Content = UiText.Get(updating ? "UpdatePlugin" : "AddPlugin"),
            IsDefault = true,
            MinWidth = 96,
            Height = 32,
            Margin = new Thickness(8, 0, 0, 0)
        };
        var cancel = new Button
        {
            Content = UiText.Get("Cancel"),
            IsCancel = true,
            MinWidth = 88,
            Height = 32,
            Margin = new Thickness(8, 0, 0, 0)
        };
        accept.Click += (_, _) =>
        {
            var source = pathInput.Text.Trim();
            if (source.Length == 0 || (!Directory.Exists(source) && !File.Exists(source)))
            {
                MessageBox.Show(dialog, UiText.Get("PluginSourceRequired"), "AgentDock", MessageBoxButton.OK, MessageBoxImage.Warning);
                pathInput.Focus();
                return;
            }
            dialog.DialogResult = true;
        };

        var sourceButtons = new StackPanel { Orientation = Orientation.Horizontal };
        sourceButtons.Children.Add(chooseFolder);
        sourceButtons.Children.Add(chooseZip);
        var body = new StackPanel { Margin = new Thickness(18) };
        body.Children.Add(new TextBlock
        {
            Text = updating
                ? UiText.Format("UpdatePluginSourceHelp", pluginName!)
                : UiText.Get("PluginSourceHelp"),
            TextWrapping = TextWrapping.Wrap,
            Foreground = new SolidColorBrush(Color.FromRgb(102, 112, 133))
        });
        body.Children.Add(pathInput);
        body.Children.Add(sourceButtons);

        var rightButtons = new StackPanel
        {
            Orientation = Orientation.Horizontal,
            HorizontalAlignment = HorizontalAlignment.Right,
            Margin = new Thickness(18, 10, 18, 18)
        };
        rightButtons.Children.Add(accept);
        rightButtons.Children.Add(cancel);
        var layout = new DockPanel();
        DockPanel.SetDock(rightButtons, Dock.Bottom);
        layout.Children.Add(rightButtons);
        layout.Children.Add(body);
        dialog.Content = layout;
        dialog.Loaded += (_, _) => pathInput.Focus();
        return dialog.ShowDialog() == true ? pathInput.Text.Trim() : null;
    }

    private sealed record CapabilityToggleTarget(string Kind, string Name, string Plugin);
}
