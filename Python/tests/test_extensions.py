import json
import subprocess
import os
from pathlib import Path
import sys
import threading
import time
import unittest
ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'Python'))
from stream_dock_manager_sdk import HostInfo
from stream_dock_manager_sdk.authoring import Display, Presentation, present, compact_display, Plugin, Action
from stream_dock_manager_sdk.testkit import Recorder
class ExtensionsTests(unittest.TestCase):
 # Verify discovery packaged controller
 def test_discovery_packaged_controller(self):
  from stream_dock_manager_sdk.inspectors import assets
  subprocess.run([os.environ.get('STREAM_DOCK_MANAGER_NODE','node'),str(ROOT/'tests/discovery.mjs'),'-'],input=assets()['inspector-workflows.js'],check=True)

 # Verify shared semantic compact layouts
 def test_shared_semantic_compact_layouts(self):
  fixture=json.loads((ROOT/'contracts/fixtures/extensions-v1.json').read_text())
  for case in fixture['layouts']:
   content=dict(case['content']);content['show_progress']=content.pop('showProgress',False)
   base=Display(title='Full title',image='icon.svg',layout='custom.json',feedback={'custom':{'value':'kept'}})
   result=compact_display(present(base,Presentation(**content),case['width'],case['height']),case['width'],case['height'])
   self.assertEqual(base.feedback,{'custom':{'value':'kept'}});self.assertEqual(result.title,base.title);self.assertEqual(result.layout,base.layout)
   for key,rect in case['rects'].items():self.assertEqual(result.feedback[key]['rect'],rect,case['name'])
   if 'progress' in case:self.assertEqual(result.feedback['indicator']['value'],case['progress'])
 # Verify observation receipt isolation
 def test_observation_receipt_isolation(self):
  for failure in [False,True]:
   recorder=Recorder(HostInfo.parse(json.loads((ROOT/'contracts/fixtures/protocol-v1.json').read_text())['manager']))
   plugin=Plugin();release=threading.Event();applied=[]
   # Execute the queued extensions tests operation
   def work(cancel):
    if not release.wait(2):raise TimeoutError()
    if failure:raise ValueError('read failed')
    return 42
   # Apply the completed extensions tests update
   def apply(p,c,result):applied.append(result)
   owner=self
   # Dispatch the incoming event through extensions tests
   def handle(p,c,e):
    owner.assertIsNotNone(p.observe(c,'read',e.context,work,apply))
    owner.assertIsNone(p.observe(c,'read',e.context,work,apply))
   plugin.register('command',Action(handle=handle))
   recorder.replay(plugin,[dict(event='keyDown',action='command',context='key',streamDockManager=dict(request_id='command'))])
   self.assertEqual([e['payload']['success'] for e in recorder.drain() if e['event']=='actionResult'],[True])
   release.set()
   for _ in range(100):
    time.sleep(.001);recorder.poll(plugin)
    if applied:break
   self.assertEqual(len(applied),1);self.assertEqual(applied[0][1] is not None,failure)
   self.assertFalse(any(e['event']=='actionResult' for e in recorder.drain()))
   plugin.close();recorder.client.close()

 # Verify thumbnail shared formats and limits
 def test_thumbnail_shared_formats_and_limits(self):
  import base64
  from stream_dock_manager_sdk.thumbnail import decode, MAX_INPUT_BYTES
  for case in json.loads((ROOT/'contracts/fixtures/extensions-v1.json').read_text())['thumbnails']:
   data=base64.b64decode(case['data']);image=decode(data,case['width'],case['height'])
   self.assertEqual((image.width,image.height),(case['width'],case['height']))
   self.assertEqual(len(image.data),len(case['rgba']))
   for i,(actual,expected) in enumerate(zip(image.data,case['rgba'])):
    if i%4!=3 and case['rgba'][i-i%4+3]==0:continue
    self.assertLessEqual(abs(actual-expected),case['tolerance'],(case['name'],i,actual,expected))
   with self.assertRaises(Exception):decode(data,321,1)
   stop=threading.Event();stop.set()
   with self.assertRaisesRegex(RuntimeError,'cancelled'):decode(data,2,2,stop)
   class MidCancel:
    count=0
    # Check whether the cancellation or synchronization signal is set
    def is_set(self):self.count+=1;return self.count>3
   with self.assertRaisesRegex(RuntimeError,'cancelled'):decode(data,2,2,MidCancel())
  for data in [b'',b'invalid',bytes(MAX_INPUT_BYTES+1)]:
   with self.assertRaises(Exception):decode(data,1,1)
