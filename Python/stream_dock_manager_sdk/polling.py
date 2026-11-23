"""Shared resource subscriptions and explicit clocks; no implicit background I/O."""
from copy import deepcopy
from dataclasses import dataclass
from email.utils import parsedate_to_datetime
import math
import re


UPDATE_INTERVAL_KEY = 'update_interval_ms'
NEVER = float('inf')

# Resolve interval
def resolve_interval(settings, policy):
    default, minimum, maximum = (policy[k] for k in ('default_ms', 'minimum_ms', 'maximum_ms'))
    # Return integer for polling
    def integer(v): return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) and int(v) == v
    if not all(integer(v) for v in (default, minimum, maximum)) or minimum < 1 or maximum < minimum or maximum > 86400000 or (default != 0 and not minimum <= default <= maximum): raise ValueError('invalid refresh policy')
    value = settings.get(UPDATE_INTERVAL_KEY, default)
    if not integer(value) or value < 0 or (value != 0 and not minimum <= value <= maximum): raise ValueError('update interval is outside the supported range')
    return value / 1000

# Update interval
def update_interval(settings, fallback):
    try: return resolve_interval(settings, {'default_ms': fallback * 1000, 'minimum_ms': 1, 'maximum_ms': 86400000})
    except (ValueError, TypeError): return fallback

# Return the next scheduled update deadline
def next_update(now, interval): return NEVER if interval == 0 else now + interval

# Retry delay
def retry_delay(interval, failures, jitter):
    backoff = min(900, 2 * 2 ** min(max(failures - 1, 0), 10))
    return max(interval, min(900, backoff * (1 + .25 * min(1, max(0, jitter)))))


# Largest whole-second duration supported by Go, shared across all SDKs
_MAXIMUM_RETRY_AFTER = 9223372036
_HTTP_DATE = re.compile(r'^((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)), ([0-9]{2}) ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)) ([0-9]{4}) ([0-9]{2}:[0-9]{2}:[0-9]{2}) GMT$')
_RFC850_DATE = re.compile(r'^((?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)), ([0-9]{2})-((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec))-([0-9]{2}) ([0-9]{2}:[0-9]{2}:[0-9]{2}) GMT$')
_ASCTIME_DATE = re.compile(r'^((?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)) ((?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)) ([ 0-9][0-9]) ([0-9]{2}:[0-9]{2}:[0-9]{2}) ([0-9]{4})$')

# Parse a protocol date into the scheduler time representation
def _http_date(value):
    parts = _HTTP_DATE.fullmatch(value)
    if parts:
        weekday, day, month, year, clock = parts.groups()
    else:
        parts = _RFC850_DATE.fullmatch(value)
        if parts:
            weekday, day, month, year, clock = parts.groups()
            year = str(int(year) + (2000 if int(year) < 70 else 1900))
        else:
            parts = _ASCTIME_DATE.fullmatch(value)
            if not parts: return None
            weekday, month, day, clock, year = parts.groups()
            day = day.strip().zfill(2)
    if int(year) < 1970: return None
    return f'{weekday[:3]}, {day} {month} {year} {clock} GMT'

# Retry after
def retry_after(value, now):
    value = value.strip(' \t\r\n')
    if value and value.isascii() and value.isdecimal():
        digits = value.lstrip('0') or '0'
        # Bound before int conversion, including Python's integer digit limit
        seconds = _MAXIMUM_RETRY_AFTER if len(digits) > 10 else min(int(digits), _MAXIMUM_RETRY_AFTER)
        return now + seconds
    canonical = _http_date(value)
    if canonical is None: return None
    try:
        date = parsedate_to_datetime(canonical)
        # format_datetime is locale-independent and includes the actual weekday
        from email.utils import format_datetime
        if format_datetime(date, usegmt=True) != canonical: return None
        return now + min(_MAXIMUM_RETRY_AFTER, max(0, date.timestamp() - now))
    except (TypeError, ValueError, OverflowError): return None


@dataclass
class PollState:
    next: float = 0
    failures: int = 0
    last_value: object = None
    stale: bool = False
    observed_at: float | None = None

    # Record completion of the pending poll state operation
    def complete(self, value, error, now, interval, jitter=0, retry_at=None):
        self._complete(None if error is not None else deepcopy(value), error, now, interval, jitter, retry_at)

    def _complete(self, value, error, now, interval, jitter=0, retry_at=None):
        if error is not None:
            self.failures = min(16, self.failures + 1)
            self.stale = self.observed_at is not None
            self.next = now + retry_delay(interval, self.failures, jitter)
        else:
            self.failures, self.stale = 0, False
            self.last_value, self.observed_at = value, now
            self.next = next_update(now, interval)
        if retry_at is not None: self.next = max(self.next, retry_at)


class Resources:
    # Initialize shared resource bindings, generations, and poll cooldowns
    def __init__(self):
        self.bindings, self.states, self.pending = {}, {}, {}
        self.sequence = 0
        self.delivered = {}
        self.cooldowns = {}
    # Watch resources data
    def watch(self, context, key, interval):
        if not key or not math.isfinite(interval) or interval < 0: raise ValueError('resource needs a key and nonnegative interval')
        if context in self.bindings and self.bindings[context][0] != key: self.unwatch(context)
        prior = self.bindings.get(context)
        if context not in self.delivered: self.delivered[context] = deepcopy(self.states.get(key, PollState()))
        state = self.delivered[context]
        if (not prior or prior[1] != interval) and state.observed_at is not None: state.next = next_update(state.observed_at, interval)
        self.bindings[context] = (key, interval)
        self.states.setdefault(key, PollState())
        if (not prior or prior[1] != interval) and self.states[key].next != 0: self._schedule(key)
    # Stop watching resources data
    def unwatch(self, context):
        prior = self.bindings.pop(context, None)
        self.delivered.pop(context, None)
        if prior and not any(key == prior[0] for key, _ in self.bindings.values()):
            self.states.pop(prior[0], None)
            self.pending.pop(prior[0], None)
            self.cooldowns.pop(prior[0], None)
        elif prior and self.states[prior[0]].next != 0: self._schedule(prior[0])
    # Return due for resources
    def due(self, now): return sorted(key for key, state in self.states.items() if key not in self.pending and now >= state.next and now >= self.cooldowns.get(key, 0))
    # Calculate the deadline for the pending operation
    def deadline(self, key):
        return max(self.states[key].next, self.cooldowns.get(key, 0)) if key in self.states else NEVER
    # Start a new generation of tracked work
    def begin(self, key):
        if key not in self.states or key in self.pending: raise ValueError('resource is absent or already running')
        self.sequence += 1
        generation = self.pending[key] = self.sequence
        return generation
    # Record completion of the pending resources operation
    def complete(self, key, generation, value, error, now, jitter=0, retry_at=None):
        if key not in self.states or self.pending.get(key) != generation: return False
        self.pending.pop(key)
        if retry_at is not None and retry_at > now: self.cooldowns[key] = retry_at
        else: self.cooldowns.pop(key, None)
        shared = None if error is not None else deepcopy(value)
        forced = self.states[key].next == 0
        for context, (resource, interval) in self.bindings.items():
            state = self.delivered[context]
            if resource != key or (not forced and now < state.next): continue
            state._complete(shared, error, now, interval, jitter, retry_at)
            if interval == 0: state.next = NEVER
        self.states[key].complete(value, error, now, 0, jitter, retry_at)
        self._schedule(key)
        return True
    # Return snapshot for resources
    def snapshot(self, context):
        return deepcopy(self.delivered.get(context))
    # Schedule resources data
    def _schedule(self, key):
        if key in self.states: self.states[key].next = max(self.cooldowns.get(key, 0), min((self.delivered[context].next for context, (resource, _) in self.bindings.items() if resource == key), default=NEVER))
