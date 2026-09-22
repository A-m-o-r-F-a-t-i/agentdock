namespace AgentDock.ControlPanel;

internal static class SidebarOrdering
{
    internal static readonly TimeSpan MinimumActivityDifference = TimeSpan.FromSeconds(60);

    // Adjacent promotions preserve an existing total order. A threshold comparator
    // would be non-transitive and could oscillate as two projects alternate calls.
    internal static List<T> Stable<T>(IEnumerable<string> previous, IEnumerable<T> incoming,
        Func<T, string> id, Func<T, DateTimeOffset?> activity, Func<T, bool>? pinned = null)
    {
        var remaining = incoming.ToDictionary(id, StringComparer.Ordinal);
        var ordered = new List<T>();
        foreach (var key in previous.Distinct(StringComparer.Ordinal))
            if (remaining.Remove(key, out var item)) ordered.Add(item);
        ordered.AddRange(remaining.Values.OrderByDescending(item => pinned?.Invoke(item) ?? false)
            .ThenByDescending(activity).ThenBy(id, StringComparer.Ordinal));
        for (var i = 1; i < ordered.Count; i++)
        {
            var position = i;
            while (position > 0)
            {
                var ahead = ordered[position - 1]; var candidate = ordered[position];
                var candidatePinned = pinned?.Invoke(candidate) ?? false;
                var aheadPinned = pinned?.Invoke(ahead) ?? false;
                var promote = candidatePinned != aheadPinned ? candidatePinned :
                    activity(candidate) is { } newer && (activity(ahead) is not { } older || newer - older >= MinimumActivityDifference);
                if (!promote) break;
                (ordered[position - 1], ordered[position]) = (candidate, ahead);
                position--;
            }
        }
        return ordered;
    }
}
