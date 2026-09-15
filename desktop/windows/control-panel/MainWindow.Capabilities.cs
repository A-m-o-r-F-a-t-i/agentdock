using System.Diagnostics;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using Button = System.Windows.Controls.Button;
using CheckBox = System.Windows.Controls.CheckBox;
using MessageBox = System.Windows.MessageBox;
using Brushes = System.Windows.Media.Brushes;
using Color = System.Windows.Media.Color;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private readonly SemaphoreSlim _capabilityGate = new(1, 1);
    private CapabilityInventory _capabilityInventory = new();
    private bool _updatingCapabilities;
    private readonly HashSet<string> _expandedPlugins = new(StringComparer.Ordinal);
    private int _capabilityLoadGeneration;
    private bool _capabilityControlsEnabled = true;

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
                // Retain the last displayed inventory for read-only inspection.
                RenderCapabilityInventory();
                SetCapabilityControlsEnabled(false);
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
        var generation = ++_capabilityLoadGeneration;
        SetCapabilityControlsEnabled(false);
        CapabilityStatusText.Text = UiText.Get("LoadingCapabilities");
        var progress = new Progress<CapabilityInventoryUpdate>(update =>
        {
            if (generation != _capabilityLoadGeneration) return;
            MergeCapabilitySection(update.Section, update.Inventory);
            RenderCapabilityInventory();
            CapabilityStatusText.Text = UiText.Get("LoadingCapabilities") + " " + CapabilityInventoryStatus();
        });
        try
        {
            var inventory = await _runtime.GetCapabilityInventoryAsync(progress);
            ++_capabilityLoadGeneration; // Ignore queued notifications after this final snapshot.
            foreach (var section in new[] { "plugins", "skills", "mcp" }) MergeCapabilitySection(section, inventory);
            RenderCapabilityInventory();
            CapabilityStatusText.Text = CapabilityInventoryStatus();
        }
        finally
        {
            if (generation == _capabilityLoadGeneration) ++_capabilityLoadGeneration;
            SetCapabilityControlsEnabled(true);
        }
    }

    private void MergeCapabilitySection(string section, CapabilityInventory inventory)
    {
        if (inventory.Errors.TryGetValue(section, out var error))
        {
            _capabilityInventory.Errors[section] = error;
            return;
        }
        _capabilityInventory.Errors.Remove(section);
        switch (section)
        {
            case "plugins": _capabilityInventory.Plugins = inventory.Plugins; break;
            case "skills": _capabilityInventory.Skills = inventory.Skills; break;
            case "mcp": _capabilityInventory.McpServers = inventory.McpServers; break;
        }
    }

    private string CapabilityInventoryStatus()
    {
        var text = UiText.Format("CapabilitiesLoaded", _capabilityInventory.Plugins.Count,
            _capabilityInventory.Skills.Count, _capabilityInventory.McpServers.Count);
        if (_capabilityInventory.Errors.Count > 0)
            text += " · " + string.Join(" | ", _capabilityInventory.Errors.Select(error => $"{error.Key}: {error.Value}"));
        return text;
    }

    private void SetCapabilityControlsEnabled(bool enabled)
    {
        // Expanders and read-only metadata remain usable while status is pending.
        _capabilityControlsEnabled = enabled;
        RenderCapabilityInventory();
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
        var timer = Stopwatch.StartNew();
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
            // Render logging is sent to the worker; file I/O never blocks WPF.
            var elapsed = timer.ElapsedMilliseconds;
            _ = Task.Run(() => _runtime.RecordCapabilityRenderTime(elapsed));
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
            IsEnabled = _capabilityControlsEnabled,
            Tag = plugin.Name,
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(14, 0, 0, 0)
        };
        toggle.Checked += PluginToggle_Changed;
        toggle.Unchecked += PluginToggle_Changed;

        var heavy = new CheckBox
        {
            Content = "Heavy", IsChecked = plugin.Heavy, Tag = plugin.Name,
            IsEnabled = _capabilityControlsEnabled,
            ToolTip = UiText.Get("HeavyPluginHelp"),
            VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(14, 0, 0, 0)
        };
        heavy.Checked += PluginHeavy_Changed;
        heavy.Unchecked += PluginHeavy_Changed;
        Grid.SetColumn(heavy, 2);
        header.Children.Add(heavy);
        Grid.SetColumn(title, 0);
        Grid.SetColumn(remove, 1);
        Grid.SetColumn(toggle, 3);
        header.Children.Add(title);
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
        var expander = new Expander
        {
            Header = UiText.Format("PluginMemberSummary", plugin.Skills?.Count ?? 0, plugin.McpServers?.Count ?? 0),
            IsExpanded = _expandedPlugins.Contains(plugin.Name), Margin = new Thickness(0, 5, 0, 0)
        };
        expander.Expanded += (_, _) =>
        {
            _expandedPlugins.Add(plugin.Name);
            if (expander.Content is null)
            {
                var previous = _updatingCapabilities;
                _updatingCapabilities = true;
                try { expander.Content = BuildPluginDetails(plugin); }
                finally { _updatingCapabilities = previous; }
            }
        };
        expander.Collapsed += (_, _) => _expandedPlugins.Remove(plugin.Name);
        if (expander.IsExpanded) expander.Content = BuildPluginDetails(plugin);
        content.Children.Add(expander);
        if ((plugin.Diagnostics?.Count ?? 0) > 0)
        {
            content.Children.Add(new TextBlock { Text = UiText.Format("PluginDiagnosticCount", plugin.Diagnostics!.Count),
                Foreground = new SolidColorBrush(Color.FromRgb(180, 35, 24)), TextWrapping = TextWrapping.Wrap });
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

    private StackPanel BuildPluginDetails(PluginCapabilityInfo plugin)
    {
        var details = new StackPanel();
        details.Children.Add(new TextBlock
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
            details.Children.Add(BuildPluginSectionTitle(UiText.Get("PluginSkills")));
            foreach (var name in (plugin.Skills ?? []).OrderBy(value => value, StringComparer.OrdinalIgnoreCase))
            {
                details.Children.Add(skillsByName.TryGetValue(name, out var skill)
                    ? BuildSkillCapabilityRow(skill, nested: true, pluginName: plugin.Name)
                    : BuildUnavailableCapabilityRow("Skill", name));
            }
        }
        if ((plugin.McpServers?.Count ?? 0) > 0)
        {
            details.Children.Add(BuildPluginSectionTitle(UiText.Get("PluginMcpServers")));
            foreach (var name in (plugin.McpServers ?? []).OrderBy(value => value, StringComparer.OrdinalIgnoreCase))
            {
                details.Children.Add(mcpByName.TryGetValue(name, out var server)
                    ? BuildMcpCapabilityRow(server, nested: true, pluginName: plugin.Name)
                    : BuildUnavailableCapabilityRow("MCP", name));
            }
        }

        foreach (var diagnostic in plugin.Diagnostics ?? [])
        {
            details.Children.Add(new TextBlock { Text = diagnostic, TextWrapping = TextWrapping.Wrap,
                Foreground = new SolidColorBrush(Color.FromRgb(180, 35, 24)), Margin = new Thickness(0, 5, 0, 0) });
        }
        return details;
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
            IsEnabled = _capabilityControlsEnabled,
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

    private async void PluginHeavy_Changed(object sender, RoutedEventArgs e)
    {
        if (_updatingCapabilities || sender is not CheckBox toggle || toggle.Tag is not string name) { return; }
        await ExecuteCapabilityActionAsync(UiText.Format("SavingPlugin", name),
            () => _runtime.SetPluginHeavyAsync(name, toggle.IsChecked == true));
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

    private sealed record CapabilityToggleTarget(string Kind, string Name, string Plugin);
}
