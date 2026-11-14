import json
import sys
import time
from stream_dock_manager_sdk.authoring import Renderer,Display
from stream_dock_manager_sdk.testkit import Recorder
frames,surfaces=map(int,sys.argv[1:])
if not 1<=frames<=1200 or not 1<=surfaces<=14: raise ValueError('workload outside bounds')
renderer,recorder=Renderer(),Recorder();started=time.monotonic();count=0
for frame in range(frames):
    for surface in range(surfaces):
        for _ in range(2): renderer.render(recorder.client,f'fixture-{surface}',Display(title=str(frame)))
        events=recorder.drain()
        if len(events)!=(2 if frame==0 else 1): raise ValueError('unexpected or duplicate display events')
        count+=len(events)
print(json.dumps(dict(schema_version=1,measurement_scope='sdk_authoring_inprocess',implementation='python',frames=frames,surfaces=surfaces,received_events=count,display_revisions=frames*surfaces,elapsed_ms=(time.monotonic()-started)*1000)))
recorder.close()
