"""Equivalent telemetry, dial, touch and deferred work example. No desktop effects."""
from stream_dock_manager_sdk import Handler, run


class Advanced(Handler):
# Start the accumulated dial position at zero
    def __init__(self): self.ticks = 0

# Demonstrate capability-gated telemetry, dial input, touch and deferred feedback
    def handle(self, client, event):
        if event.event == 'willAppear':
            if client.supports('telemetry-v1'):
                client.subscribe_telemetry(['cpu'])
            else:
                client.title(event.context, 'Telemetry unavailable on this host')
        elif event.event == 'dialRotate':
            self.ticks += event.payload.get('ticks', 0)
            client.title(event.context, str(self.ticks))
        elif event.event == 'touchTap':
            client.title(event.context, 'Touched')
        elif event.event == 'keyUp':
# Return a background result only while its binding remains uncancelled
            def work(cancel):
                if cancel.is_set(): raise RuntimeError('cancelled')
                return 'Background result'
            client.background(event.context, work, lambda client, value: client.title(event.context, value))

# Disable periodic polling because this example reacts only to events
    def next_poll(self, now): return None
# Allow adjacent dial events to be delivered as accumulated ticks
    def batch_dial(self, action): return True


if __name__ == '__main__': run(Advanced())
