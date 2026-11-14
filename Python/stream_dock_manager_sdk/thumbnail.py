"""Optional Pillow decoder. Use from a background job, never the event loop.

Sampling uses the top-left pixel of each cell, without EXIF rotation or animation.
Cancellation is cooperative at reads, rows, and result publication. Codec-internal
CPU work completes before the next check; cancelled results are never returned.
"""
from dataclasses import dataclass
from io import BytesIO
MAX_INPUT_BYTES=64<<20
MAX_SOURCE_PIXELS=32000000
MAX_DIMENSION=320
@dataclass(frozen=True)
class Limits:
    max_input_bytes:int=MAX_INPUT_BYTES
    max_source_pixels:int=MAX_SOURCE_PIXELS
    # Validate and normalize limits after construction
    def __post_init__(self):
        if any(type(v) is not int or not 0<v<=9007199254740991 for v in (self.max_input_bytes,self.max_source_pixels)):raise ValueError('invalid thumbnail limits')
@dataclass(frozen=True)
class Thumbnail:
    width:int
    height:int
    data:bytes

# Decode thumbnail data
def _decode(data,width,height,cancel=None,fit_within=False,limits=Limits()):
    # Check thumbnail data
    def check():
        if cancel is not None and cancel.is_set(): raise RuntimeError('thumbnail cancelled')
    check()
    if not isinstance(data,(bytes,bytearray,memoryview)) or not 0<len(data)<=limits.max_input_bytes or type(width) is not int or type(height) is not int or not 0<width<=MAX_DIMENSION or not 0<height<=MAX_DIMENSION:
        raise ValueError('thumbnail input or output exceeds supported limits')
    try:from PIL import Image
    except ImportError as error:raise RuntimeError('Install the SDK thumbnail extra to decode images') from error
    class Reader(BytesIO):
        # Read the next reader value or data segment
        def read(self,size=-1):check();return super().read(size)
        # Move the fixture stream to the requested position
        def seek(self,*args):check();return super().seek(*args)
    with Reader(data) as reader, Image.open(reader,formats=['JPEG','PNG','GIF']) as source:
        sw,sh=source.size
        if sw<=0 or sh<=0 or sw*sh>limits.max_source_pixels:raise ValueError('invalid thumbnail source dimensions')
        if fit_within:width,height=fit(sw,sh,width,height,limits=limits)
        offset=(0,0,sw,sh)
        if source.format=='GIF':
            offset=source.tile[0][1]
            if not (0<=offset[0]<offset[2]<=sw and 0<=offset[1]<offset[3]<=sh):raise ValueError('invalid GIF frame bounds')
        source.load();check()
        if source.mode in ('I','I;16','I;16B'):
            source=source.convert('I').point(lambda v:v/257).convert('L')
        rgba=source.convert('RGBA');pixels=rgba.load();out=bytearray(width*height*4)
        left,top,right,bottom=offset
        for y in range(height):
            check();sy=y*sh//height
            for x in range(width):
                sx=x*sw//width
                if sx>=right-left or sy>=bottom-top:continue
                at=(y*width+x)*4;out[at:at+4]=bytes(pixels[sx+left,sy+top])
        check();return Thumbnail(width,height,bytes(out))

# Fit thumbnail data
def fit(width,height,max_width,max_height,*,limits=Limits()):
    if any(type(v) is not int for v in (width,height,max_width,max_height)) or width<1 or height<1 or width*height>limits.max_source_pixels or not 0<max_width<=MAX_DIMENSION or not 0<max_height<=MAX_DIMENSION:raise ValueError('invalid thumbnail fit dimensions')
    if width<=max_width and height<=max_height:return width,height
    return (max_width,max(1,height*max_width//width)) if max_width*height<=max_height*width else (max(1,width*max_height//height),max_height)
# Decode thumbnail data
def decode(data,width,height,cancel=None,*,limits=Limits()):return _decode(data,width,height,cancel,limits=limits)
# Fit decode
def fit_decode(data,max_width,max_height,cancel=None,*,limits=Limits()):return _decode(data,max_width,max_height,cancel,True,limits)
# Encode the image into a bounded PNG payload
def png(source,cancel=None):
    if cancel is not None and cancel.is_set():raise RuntimeError('thumbnail cancelled')
    if type(source.width) is not int or type(source.height) is not int or not 0<source.width<=MAX_DIMENSION or not 0<source.height<=MAX_DIMENSION or len(source.data)!=source.width*source.height*4:raise ValueError('invalid thumbnail pixels')
    from PIL import Image
    with Image.frombytes('RGBA',(source.width,source.height),bytes(source.data)) as image, BytesIO() as output:
        image.save(output,format='PNG');data=output.getvalue()
    if cancel is not None and cancel.is_set():raise RuntimeError('thumbnail cancelled')
    return data
# Encode image bytes into a portable data URI
def data_uri(source,cancel=None):
    import base64
    return 'data:image/png;base64,'+base64.b64encode(png(source,cancel)).decode('ascii')
@dataclass(frozen=True)
class Rendered:
    width:int
    height:int
    data_uri:str
# Render thumbnail data
def render(data,max_width,max_height,cancel=None,*,limits=Limits()):
    image=fit_decode(data,max_width,max_height,cancel,limits=limits)
    return Rendered(image.width,image.height,data_uri(image,cancel))
