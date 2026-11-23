"""Optional binding runtime, display revisions, view relationships and keyed jobs."""
from copy import deepcopy
from dataclasses import dataclass, field
import time
import re
from .runtime import Handler
from .settings import merge


@dataclass
class Display:
    title: str = ''
    image: str = ''
    state: int = 0
    layout: str = ''
    feedback: dict = field(default_factory=dict)


# Compare feedback values without losing numeric precision
def _feedback_equal(a, b):
    # bool subclasses int in Python, but they are distinct JSON types
    if isinstance(a, bool) or isinstance(b, bool): return type(a) is type(b) and a == b
    if isinstance(a, (int, float)) or isinstance(b, (int, float)):
        if not isinstance(a, (int, float)) or not isinstance(b, (int, float)): return False
        if type(a) is type(b): return a == b
        # Preserve the decimal JSON values, including integers beyond float precision
        return _decimal_parts(str(a)) == _decimal_parts(str(b))
    if isinstance(a, dict) or isinstance(b, dict):
        return isinstance(a, dict) and isinstance(b, dict) and a.keys() == b.keys() and all(_feedback_equal(value, b[key]) for key, value in a.items())
    if isinstance(a, list) or isinstance(b, list):
        return isinstance(a, list) and isinstance(b, list) and len(a) == len(b) and all(_feedback_equal(x, y) for x, y in zip(a, b))
    return a == b


# Separate the decimal sign and digits for stable formatting
def _decimal_parts(value):
    negative = value.startswith('-')
    value = value.removeprefix('-').lower()
    mantissa, _, exponent = value.partition('e')
    scale = int(exponent or '0')
    whole, _, fraction = mantissa.partition('.')
    digits = (whole + fraction).lstrip('0')
    if not digits: return ('0', 0)
    trimmed = digits.rstrip('0')
    return (('-' if negative else '') + trimmed, scale - len(fraction) + len(digits) - len(trimmed))


# Translate display state into host protocol messages
def display_messages(context, previous, desired, rich=True, states=True):
    known = previous is not None
    old = previous or Display()
    layout = not known or old.layout != desired.layout
    state = states and (not known or old.state != desired.state)
    messages = []
    # Send the supplied authoring message
    def send(event, payload): messages.append(dict(event=event, context=context, payload=payload))
    if rich and layout and desired.layout: send('setFeedbackLayout', dict(layout=desired.layout))
    if state: send('setState', dict(state=desired.state))
    if (not known and desired.image) or (known and old.image != desired.image) or (state and desired.image): send('setImage', dict(image=desired.image, target=0))
    if not known or old.title != desired.title or state: send('setTitle', dict(title=desired.title, target=0))
    if rich:
        feedback = {key: deepcopy(value) for key, value in desired.feedback.items() if layout or key not in old.feedback or not _feedback_equal(old.feedback[key], value)}
        feedback.update({key: None for key in old.feedback if not layout and key not in desired.feedback})
        if feedback: send('setFeedback', feedback)
    return messages


# Resolve the asset variant for the selected theme
def theme_asset(value, theme):
    if isinstance(value, str):
        return re.sub(r'-(?:light|dark)(\.(?:svg|json))$', lambda match: '-' + theme + match[1], value)
    if isinstance(value, dict): return {key: theme_asset(item, theme) for key, item in value.items()}
    if isinstance(value, list): return [theme_asset(item, theme) for item in value]
    return value


class Renderer:
    # Start with no retained render output for change detection
    def __init__(self): self.last = {}
    # Reset renderer data
    def reset(self, context=None):
        if context is None: self.last.clear()
        else: self.last.pop(context, None)
    # Render renderer data
    def render(self, client, context, desired):
        if not isinstance(desired, Display): raise TypeError('render callback must return Display')
        previous = self.last.get(context)
        if previous == desired: return
        for message in display_messages(context, previous, desired, True):
            client.send(message['event'], context, message['payload'])
        self.last[context] = deepcopy(desired)


@dataclass
class Binding:
    action: str
    settings: dict = field(default_factory=dict)
    surface: object = None


@dataclass
class ViewTarget:
    action: str
    updated_by: tuple = ()
    shared: bool = False
    live: bool = True
    after_activation: bool = False


# Check whether can update
def can_update(bindings, targets, active, context, origin='', action='', automatic=False, lifecycle=False, global_settings=None):
    if context not in bindings: return False
    rule = targets.get(bindings[context].action, ViewTarget(bindings[context].action))
    live = rule.live and (not rule.after_activation or context in active)
    if origin and not automatic and action and (origin not in bindings or bindings[origin].action != action): return False
    if origin == context: return not lifecycle or live or context in active
    if automatic: return live
    if not origin or action not in rule.updated_by or rule.after_activation and context not in active: return False
    if rule.shared:
        if origin not in bindings: return False
        left = merge(global_settings or {}, bindings[origin].settings).get('shared_id')
        right = merge(global_settings or {}, bindings[context].settings).get('shared_id')
        return isinstance(left, str) and bool(left) and left == right
    return action != bindings[context].action


@dataclass
class Action:
    handle: object = None
    render: object = None
    appeared: object = None
    changed: object = None
    disappeared: object = None


class Plugin(Handler):
    # Register action handlers and initialize binding, settings, and job state
    def __init__(self, *, targets=(), store=None, clock=time.monotonic):
        self.actions, self.bindings, self.global_settings, self.active = {}, {}, {}, set()
        self.targets = {target.action: target for target in targets}
        self.renderer, self.store, self.clock = Renderer(), store, clock
        self.jobs, self.generation, self._scope = {}, 0, ('', '', True, False)
        self._client = None
        self._secret_fields = None
        self.inspector = None

    # Register plugin data
    def register(self, action, handlers):
        if not action or action in self.actions: raise ValueError('empty or duplicate action')
        self.actions[action] = handlers
        return self

    # Return effective settings for plugin
    def effective_settings(self, context): return merge(self.global_settings, self.bindings[context].settings)

    # Render plugin data
    def render(self, client, context):
        if context not in self.bindings: return
        origin, action, automatic, lifecycle = self._scope
        if not can_update(self.bindings, self.targets, self.active, context, origin, action, automatic, lifecycle, self.global_settings): return
        handlers = self.actions.get(self.bindings[context].action)
        if handlers and handlers.render:
            view = handlers.render(self, context, self.bindings[context])
            self.renderer.render(client, context, view)

    # Render targets
    def render_targets(self, client):
        for context in sorted(self.bindings): self.render(client, context)

    # Clear plugin data
    def clear(self, client, context):
        for key, job in list(self.jobs.items()):
            if job['context'] == context: self.cancel_job(client, key)
        self.renderer.reset(context)

    # Dispatch the incoming event through plugin
    def handle(self, client, event):
        self._client = client
        if event.event in ('willAppear', 'didReceiveSettings', 'didReceiveGlobalSettings'):
            from .credentials import safe_error, secret_settings
            if self._secret_fields is None:
                self._secret_fields = {key: secret_settings(b.settings) for key,b in self.bindings.items()}
            incoming = secret_settings(event.settings)
            snapshots = [secret_settings(self.global_settings), incoming, *self._secret_fields.values()]
            if event.event != 'didReceiveGlobalSettings':
                if incoming: self._secret_fields[event.context] = incoming
                else: self._secret_fields.pop(event.context, None)
            # Redact plugin data
            def redact(text):
                for settings in snapshots: text = str(safe_error(RuntimeError(text), settings))
                return text
            client.log_redactor = redact
        if event.event == "willDisappear" and self._secret_fields is not None:
            self._secret_fields.pop(event.context, None)
        name, context, action = event.event, event.context, event.action
        old_scope = self._scope
        lifecycle = name in ('willAppear', 'didReceiveSettings')
        phase = (event.surface.touch or {}).get('phase', '') if event.surface and event.surface.touch is not None else None
        activated = name in ('keyDown', 'dialDown') or name == 'touchTap' and (phase in ('tap', 'end', '') if phase is not None else not event.payload.get('hold', False))
        self._scope = (context, action, lifecycle, lifecycle)
        try:
            if name == 'didReceiveGlobalSettings':
                self.global_settings = deepcopy(event.settings)
                for key in list(self.jobs):
                    if self._cancel_on_global_settings(key): self.cancel_job(client, key)
                return
            if name == 'didReceivePluginTheme':
                theme = event.payload.get('resolved')
                if theme not in ('light', 'dark'): raise ValueError('unsupported theme')
                # Retheme cached feedback without resampling command state
                for key, view in list(self.renderer.last.items()):
                    view = deepcopy(view)
                    view.image, view.layout, view.feedback = (theme_asset(value, theme) for value in (view.image, view.layout, view.feedback))
                    self.renderer.reset(key)
                    self.renderer.render(client, key, view)
                return
            if not context: return
            old = self.bindings.get(context)
            if old and lifecycle and old.action != action:
                self.clear(client, context)
                self.active.discard(context)
                previous = self.actions.get(old.action)
                if previous and previous.disappeared: previous.disappeared(self, client, event)
            if name == 'willDisappear':
                self.clear(client, context)
                self.bindings.pop(context, None)
                self.active.discard(context)
                handlers = self.actions.get(old.action if old else action)
                if handlers and handlers.disappeared: handlers.disappeared(self, client, event)
                return
            if lifecycle:
                if name == 'didReceiveSettings': self.clear(client, context)
                self.bindings[context] = Binding(action, deepcopy(event.settings), event.surface or (old.surface if old else None))
            elif name in ('keyDown', 'keyUp', 'dialDown', 'dialUp', 'dialRotate', 'touchTap') and old is None:
                self.bindings[context] = Binding(action, deepcopy(event.settings), event.surface)
            handlers = self.actions.get(action or (old.action if old else ''))
            if activated: self.active.add(context)
            if name == 'sendToPlugin' and self.inspector:
                self.inspector(self, client, event)
                return
            if handlers:
                callback = handlers.appeared if name == 'willAppear' else handlers.changed if name == 'didReceiveSettings' else handlers.handle
                if callback: callback(self, client, event)
            if lifecycle: self.render(client, context)
            elif name in ('keyDown', 'keyUp', 'dialDown', 'dialUp', 'dialRotate', 'touchTap'): self.render_targets(client)
        finally: self._scope = old_scope

    # Schedule background work without blocking event delivery
    def background(self, client, key, context, work, apply, *, replace=False, track_action=True):
        if key in self.jobs:
            if not replace: raise RuntimeError('plugin action already running')
            self.cancel_job(client, key)
        if len(self.jobs) >= 32: raise RuntimeError('plugin job queue full')
        self.generation += 1
        generation, scope = self.generation, self._scope
        # Finalize the pending plugin operation
        def finish(client, value):
            job = self.jobs.get(key)
            if job is None or job['generation'] != generation: return
            self.jobs.pop(key)
            old_scope, self._scope = self._scope, scope
            try:
                if not value[0]: raise RuntimeError(value[1])
                apply(self, client, value[1])
                self.render_targets(client)
            finally: self._scope = old_scope
        # Invoke the wrapped operation with the surrounding policy
        def wrapped(cancel):
            try: return (True, work(cancel))
            except Exception as error: return (False, str(error))
        cancel = client.background(context, wrapped, finish, track_action=track_action)
        self.jobs[key] = dict(generation=generation, context=context, cancel=cancel)
        return cancel

    # Read after a command; apply receives (value, error), without a receipt
    def observe(self, client, key, context, work, apply):
        """Read after a command; apply receives (value, error), without a receipt."""
        # Read the next plugin value or data segment
        def read(cancel):
            try: return (work(cancel), None)
            except Exception as error: return (None, error)
        try:
            return self.background(client, key, context, read, apply, track_action=False)
        except RuntimeError:
            return None

    # A connector manages its shared worker when settings change.
    def _cancel_on_global_settings(self, key):
        return True

    # Cancel the outstanding job by key.
    def cancel_job(self, client, key):
        job = self.jobs.pop(key, None)
        if job: client.cancel_background(job['cancel'])

    # Poll plugin for ready work or updated state
    def poll(self, client):
        self._client = client
        self._scope = ('', '', True, False)
        self.render_targets(client)
        if self.store: self.store.flush()

    # Return the next scheduled poll deadline
    def next_poll(self, now):
        if self.store and self.store.dirty: return self.store.due
        return now + 1 if any(rule.live for rule in self.targets.values()) else None

    # Stop plugin activity and release owned resources
    def close(self):
        if self._client:
            for key in list(self.jobs): self.cancel_job(self._client, key)
        if self.store: self.store.close()


@dataclass
class Visual:
    icon: str = ''
    states: list = field(default_factory=list)


@dataclass
class DisplayStatus:
    text: str = ''
    icon: str = ''
    state: int = 0
    unavailable: bool = False


# Project display items into readable text lines
def display_lines(text, columns):
    columns = max(1, columns)
    paragraphs, lines = text.split('\n'), []
    for paragraph in paragraphs:
        if len(paragraphs) > 1 and len(paragraph) > columns:
            lines.append(paragraph[:max(0, columns - 1)] + '>'); continue
        while len(paragraph) > columns:
            end = paragraph[:columns].rfind(' ')
            if end <= 0: end = columns
            lines.append(paragraph[:end]); paragraph = paragraph[end:].strip()
            if len(lines) > 3: break
        lines.append(paragraph)
        if len(lines) > 3: break
    if len(lines) > 3:
        lines = lines[:3]; lines[2] = lines[2][:max(0, columns - 1)] + '>'
    return lines


# Project operation status into semantic display items
def status_display(status, surface=None, visual=None, theme=''):
    visual = visual or Visual()
    state = status.state if 0 <= status.state < len(visual.states) else 0
    icon = visual.states[state] if len(visual.states) > 1 else visual.icon
    icon = status.icon or icon
    if status.unavailable or not icon: icon, state = 'icons/unavailable', 0
    image = icon + ('-' + theme if theme else '') + '.svg'
    width = min(16384, surface.width) if surface and surface.width > 0 and surface.height > 0 else 112
    height = min(16384, surface.height) if surface and surface.width > 0 and surface.height > 0 else 112
    text_width = width * 192 // 200
    size = min(width * 60 // 100, height * 43 // 100)
    x, y, layout = (width - size) // 2, 0, 'key'
    if width > height * 13 // 10:
        layout, text_width = 'wide', width * 120 // 200
        size = min(width * 36 // 100, height * 80 // 100)
        x, y = width * 2 // 100, (height - size) // 2
    title = status.text[:500]
    feedback = dict(icon=dict(value=image, rect=[x*200//width, y*100//height, size*200//width, size*100//height]), indicator=dict(rect=[0,0,0,0]))
    lines = display_lines(title, max(1, text_width // (8 * min(6, max(1, height // 100)))))
    for index in range(3): feedback[f'line{index}'] = lines[index] if index < len(lines) else ''
    return Display(title=title, image=image, state=state, layout='display/' + layout + ('-' + theme if theme else '') + '.json', feedback=feedback)


# Build the feedback layout from semantic display items
def display_layout(wide=False):
    import json
    from .inspectors import assets
    return json.loads(assets()['display/' + ('wide' if wide else 'key') + '.json'])

@dataclass
class Presentation:
    kind: str = 'value'
    primary: str = ''
    label: str = ''
    detail: str = ''
    progress: float = 0
    show_progress: bool = False


# Apply a semantic layout without changing full title or feedback identities
def present(view, content, width, height):
    """Apply a semantic layout without changing full title or feedback identities."""
    import math
    if not content.kind or type(width) is not int or type(height) is not int or not 0 < width <= 16384 or not 0 < height <= 16384:
        return deepcopy(view)
    view=deepcopy(view); feedback=view.feedback
    primary=content.primary; label='' if content.label==primary else content.label
    detail='' if content.detail in (primary,label) else content.detail
    wide=width>height*13/10
    # Return field for authoring
    def field(value,rect,size):
        return dict(value=value,rect=rect if value else [*rect[:2],0,0],font=dict(size=size,fit=True))
    if wide:
        icon=min(width*24//100,height*70//100)
        feedback['icon']=dict(value=view.image,rect=[4,(100-icon*100//height)//2,icon*200//width,icon*100//height])
        feedback['line0']=field(primary,[58,8,138,48],32)
        feedback['line1']=field(label,[58,58,138,18],13)
        feedback['line2']=field(detail,[58,78,138,16],11)
    else:
        feedback['icon']=dict(value=view.image,rect=[76,2,48,24])
        feedback['line0']=field(primary,[6,28,188,40],30)
        feedback['line1']=field(label,[6,70,188,14],11)
        feedback['line2']=field(detail,[6,85,188,11],9)
    if content.kind=='command' and not primary:
        if wide:
            icon=min(width*36//100,height*80//100)
            feedback['icon']=dict(value=view.image,rect=[4,(100-icon*100//height)//2,icon*200//width,icon*100//height])
            feedback['line1']=field(label,[82,20,114,60],24)
        else:
            feedback['icon']=dict(value=view.image,rect=[45,5,110,55])
            feedback['line1']=field(label,[6,67,188,26],16)
    feedback['indicator']=dict(rect=[0,0,0,0])
    if content.show_progress and math.isfinite(content.progress): feedback['indicator']=dict(value=max(0,min(100,content.progress)),rect=[6,97,188,3])
    return view


# Build the compact display projection from semantic items
def compact_display(view,width,height):
    view=deepcopy(view); feedback=view.feedback
    if type(width) is not int or type(height) is not int or width<=0 or height<=0 or height>=80 or width>height*13//10: return view
    primary,label,icon=(feedback.get(key) for key in ('line0','line1','icon'))
    if not isinstance(primary,dict) or primary.get('value') in ('',None) or not isinstance(label,dict) or not isinstance(icon,dict): return view
    size=min(width,height)*37//100
    feedback['line2']=dict(rect=[0,0,0,0])
    icon['rect']=[(width-size)*100//width,2,size*200//width,size*100//height]
    primary['rect']=[7,40,186,34];label['rect']=[7,75,186,21];label['font']=dict(size=19,fit=True)
    return view
