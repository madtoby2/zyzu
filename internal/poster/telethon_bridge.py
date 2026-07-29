import asyncio, json, os, subprocess, sys
from telethon import TelegramClient
from telethon.tl.types import DocumentAttributeFilename, DocumentAttributeVideo

def video_attributes(file_path):
    try:
        result = subprocess.run([
            'ffprobe', '-v', 'error', '-of', 'json',
            '-show_entries', 'stream=width,height,duration:stream_tags=rotate:format=duration',
            '-select_streams', 'v:0', file_path,
        ], capture_output=True, text=True, timeout=20, check=True)
        metadata = json.loads(result.stdout)
        stream = (metadata.get('streams') or [{}])[0]
        width = int(stream.get('width') or 0)
        height = int(stream.get('height') or 0)
        rotation = int((stream.get('tags') or {}).get('rotate') or 0) % 360
        if rotation in (90, 270):
            width, height = height, width
        duration = float(stream.get('duration') or (metadata.get('format') or {}).get('duration') or 0)
        if width > 0 and height > 0:
            return [
                DocumentAttributeVideo(
                    duration=max(0, round(duration)),
                    w=width,
                    h=height,
                    supports_streaming=True,
                ),
                DocumentAttributeFilename(os.path.basename(file_path)),
            ]
    except Exception:
        pass
    return None

async def main():
    req = json.loads(sys.stdin.read())
    session = os.environ.get('ZYZU_TELETHON_SESSION', 'telethon')
    client = TelegramClient(session, int(os.environ.get('ZYZU_API_ID', '11535358')), os.environ.get('ZYZU_API_HASH', '33d372962fadb01df47e6ceed4e33cd6'))
    await client.connect()
    if req.get('action') == 'status':
        print(json.dumps({'authorized': await client.is_user_authorized()})); await client.disconnect(); return
    if req.get('action') == 'request_code':
        sent = await client.send_code_request(req['phone'])
        open(session + '.code_hash', 'w').write(sent.phone_code_hash)
        print(json.dumps({'requested': True})); await client.disconnect(); return
    if req.get('action') == 'login':
        code_hash = open(session + '.code_hash').read().strip()
        try:
            await client.sign_in(req['phone'], req['code'], phone_code_hash=code_hash)
        except Exception as exc:
            if exc.__class__.__name__ == 'SessionPasswordNeededError':
                await client.sign_in(password=req.get('password', ''))
            else: raise
        print(json.dumps({'authorized': await client.is_user_authorized()})); await client.disconnect(); return
    if not await client.is_user_authorized():
        raise RuntimeError('telethon session is not authorized')
    chat_id = int(req['chat_id']) if req.get('chat_id') is not None else None
    # Resolve the channel explicitly so Telethon uses its cached access hash
    # instead of treating a bare -100... ID as an invalid channel object.
    entity = await client.get_input_entity(chat_id) if chat_id is not None else None
    if req.get('action') == 'upload_video':
        kwargs = {
            'caption': req.get('caption', ''),
            'parse_mode': 'html',
            'supports_streaming': True,
            'force_document': False,
        }
        thumb_path = req.get('thumb_path')
        if thumb_path and os.path.isfile(thumb_path):
            kwargs['thumb'] = thumb_path
        attributes = video_attributes(req['file_path'])
        if attributes:
            kwargs['attributes'] = attributes
        msg = await client.send_file(entity, req['file_path'], **kwargs)
        print(json.dumps({'message_id': msg.id, 'thumb_submitted': bool(kwargs.get('thumb'))})); await client.disconnect(); return
    msg = await client.send_message(entity, req['text'])
    print(json.dumps({'message_id': msg.id}))
    await client.disconnect()

try:
    asyncio.run(main())
except Exception as exc:
    print(json.dumps({'error': str(exc)}))
    sys.exit(1)
