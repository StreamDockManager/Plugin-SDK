"""MIT. Counter composed from the optional SDK authoring framework."""
import base64
import signal
import threading
from stream_dock_manager_sdk import run
from stream_dock_manager_sdk.authoring import Plugin, Action, Display
from stream_dock_manager_sdk.hoststorage import PortableSettings


# Normalize counter values while preserving unrelated saved settings
def clean(value):
    settings = dict(value)
    count = settings.get('count', 0)
    settings.update(prefix=str(settings.get('prefix', 'Count'))[:80],
                    count=count if type(count) is int and 0 <= count < 2**53 else 0)
    return settings


class Counter(Plugin):
# Connect inspector input to the same counter update path
    def __init__(self):
        super().__init__()
        self.inspector = self.input

# Log completed key presses rather than every lifecycle event
    def should_log_action(self, event): return event.event == 'keyUp'

# Register unseen action IDs before delegating lifecycle handling
    def handle(self, client, event):
        if event.action and event.action not in self.actions:
            self.register(event.action, Action(handle=self.input, render=self.view, appeared=self.update, changed=self.update))
        super().handle(client, event)

# Normalize settings restored by appearance or host updates
    def update(self, plugin, client, event):
        self.bindings[event.context].settings = clean(self.bindings[event.context].settings)

# Render the count as a title, parity image and layout feedback
    def view(self, plugin, context, binding):
        settings = clean(binding.settings)
        count = settings['count']
        name = 'odd' if count % 2 else 'even'
        path = 'M16 6v20M6 16h20' if count % 2 else 'M7 9h18M7 19h18'
        image = 'data:image/svg+xml;base64,' + base64.b64encode(('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><g fill="none" stroke="#ffffff" stroke-width="3" stroke-linecap="round"><path d="%s"/></g></svg>' % path).encode('utf-8')).decode('ascii')
        return Display(title=f"{settings['prefix']} {count}", image=image,
                       state=count % 2, layout='display/counter.json',
                       feedback=dict(prefix=settings['prefix'], count=count, icon=f'icons/{name}.svg'))

# Apply key presses and inspector edits then persist and render the counter
    def input(self, plugin, client, event):
        context, payload = event.context, event.payload
        if context not in self.bindings: return
        settings = clean(self.bindings[context].settings)
        if event.event == 'keyUp': settings['count'] += 1
        elif event.event == 'sendToPlugin':
            if payload.get('reset') is True: settings['count'] = 0
            elif 'settings' in payload: settings['prefix'] = clean(payload['settings'])['prefix']
            elif payload.get('request') != 'settings': return
        else: return
        self.bindings[context].settings = settings
        if event.event == 'keyUp' or payload.get('request') != 'settings':
            PortableSettings(client, 'action', context).replace(settings)
        self.render(client, context)
        client.to_inspector(context, dict(settings=settings), action=event.action)


# Stop the host loop cooperatively on either supported termination signal
if __name__ == '__main__':
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    run(Counter(), stop=stop)
