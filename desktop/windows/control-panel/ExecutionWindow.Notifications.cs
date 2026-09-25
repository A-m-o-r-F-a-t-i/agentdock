using System.Net;
using System.Net.Http;
using System.Windows;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private readonly TaskCompletionSource _ready = new(TaskCreationOptions.RunContinuationsAsynchronously);

    internal async Task NavigateCompletionAsync(TaskNotification notification)
    {
        await _ready.Task.WaitAsync(_lifetime.Token);
        await GuardAsync(async () =>
        {
            if (notification.ConversationId.Length > 0)
            {
                try
                {
                    var result = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Escape(notification.ConversationId), _lifetime.Token);
                    var row = ExecutionObject.From(result.Field("conversation"), "conversation");
                    await SelectObjectAsync(row);
                }
                catch (HttpRequestException ex) when (ex.StatusCode is HttpStatusCode.NotFound or HttpStatusCode.Gone)
                { await SelectObjectAsync(null); }
            }
            else await SelectObjectAsync(null);
            _selectedTaskId = notification.TaskId;
            var choices = TaskChoiceCombo.ItemsSource is IEnumerable<ExecutionChoice> current ? current.ToList() : [];
            if (!choices.Any(item => item.Id == notification.TaskId)) choices.Add(new(notification.TaskId, notification.Title));
            _updating = true;
            try { TaskChoiceCombo.ItemsSource = choices; TaskChoiceCombo.SelectedValue = notification.TaskId; }
            finally { _updating = false; }
            OpenDetails(notification.Title, TaskDetailsPanel);
            await LoadTaskAsync(notification.TaskId, notification.ThreadId, true);
            if (_selected is not null)
            {
                _taskFilter = notification.TaskId;
                FilterTaskButton.Content = UiText.Get("ExecutionShowAll");
                await LoadCallsAsync(false);
            }
            else ObjectTitle.Text = notification.Title;
        });
    }
}
