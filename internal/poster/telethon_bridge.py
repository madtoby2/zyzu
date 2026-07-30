import atexit, asyncio, json, os, subprocess, sys, time
from telethon import TelegramClient, functions, types, utils
from telethon.tl.types import DocumentAttributeFilename, DocumentAttributeVideo

try:
    from aiofasttelethonhelper import fast_upload
except ImportError:
    fast_upload = None

class UploadProgress:
    def __init__(self, file_path):
        self.file_path = file_path
        self.started = time.monotonic()
        self.last_emit = 0.0

    def __call__(self, *args, **kwargs):
        done = kwargs.get('done', args[0] if args else 0) or 0
        total = kwargs.get('total', args[1] if len(args) > 1 else 0) or 0
        now = time.monotonic()
        if done < total and now - self.last_emit < 5:
            return
        self.last_emit = now
        elapsed = max(now - self.started, 0.001)
        speed = done / elapsed
        percent = done * 100 / total if total else 0
        eta = (total - done) / speed if speed > 0 and total > done else 0
        print(
            f"[upload] {os.path.basename(self.file_path)} "
            f"{percent:.1f}% ({done / 1048576:.1f}/{total / 1048576:.1f}MB) "
            f"{speed / 1048576:.2f}MB/s ETA {eta / 60:.1f}min",
            file=sys.stderr,
            flush=True,
        )

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

def cover_thumbnail(cover_path):
    thumb_path = cover_path + '.telegram-thumb.jpg'
    for quality in ('5', '10', '16'):
        result = subprocess.run([
            'ffmpeg', '-y', '-loglevel', 'error',
            '-i', cover_path,
            '-vf',
            'scale=320:180:force_original_aspect_ratio=decrease,'
            'pad=320:180:(ow-iw)/2:(oh-ih)/2:black',
            '-frames:v', '1', '-q:v', quality,
            thumb_path,
        ], capture_output=True, text=True, timeout=20)
        if result.returncode == 0 and os.path.isfile(thumb_path):
            if os.path.getsize(thumb_path) <= 200 * 1024:
                atexit.register(lambda: os.path.isfile(thumb_path) and os.remove(thumb_path))
                return thumb_path
    if os.path.isfile(thumb_path):
        os.remove(thumb_path)
    raise RuntimeError('resource cover could not be converted to a Telegram thumbnail')

async def prepare_video_media(client, video_path, thumb_path, progress):
    attributes = video_attributes(video_path)
    if fast_upload:
        print(
            f"[upload] parallel multipart started: "
            f"{os.path.basename(video_path)} ({os.path.getsize(video_path) / 1048576:.1f}MB)",
            file=sys.stderr,
            flush=True,
        )
        uploaded_file = await fast_upload(
            client=client,
            file_path=video_path,
            progress_callback=progress,
        )
        _, video_media, _ = await client._file_to_media(
            uploaded_file,
            force_document=False,
            supports_streaming=True,
            attributes=attributes,
            thumb=thumb_path,
            mime_type='video/mp4',
            nosound_video=True,
        )
        return video_media

    print(
        "[upload] aiofasttelethonhelper unavailable; using single connection",
        file=sys.stderr,
        flush=True,
    )
    _, video_media, _ = await client._file_to_media(
        video_path,
        force_document=False,
        supports_streaming=True,
        attributes=attributes,
        thumb=thumb_path,
        progress_callback=progress,
        nosound_video=True,
    )
    return video_media

async def upload_video_cover(client, entity, cover_path):
    _, cover_media, _ = await client._file_to_media(
        cover_path,
        force_document=False,
        as_image=True,
    )
    if isinstance(cover_media, (types.InputMediaUploadedPhoto, types.InputMediaPhotoExternal)):
        uploaded = await client(functions.messages.UploadMediaRequest(entity, media=cover_media))
        return utils.get_input_photo(uploaded.photo)
    if isinstance(cover_media, types.InputMediaPhoto):
        return cover_media.id
    raise TypeError(f'unsupported video cover media: {type(cover_media).__name__}')

async def send_video_with_cover(client, entity, cover_path, thumb_path, video_path, caption):
    video_cover = await upload_video_cover(client, entity, cover_path)
    video_media = await prepare_video_media(
        client,
        video_path,
        thumb_path,
        UploadProgress(video_path),
    )
    if not isinstance(video_media, (types.InputMediaUploadedDocument, types.InputMediaDocument)):
        raise TypeError(f'unsupported video media: {type(video_media).__name__}')
    video_media.video_cover = video_cover

    caption_text, caption_entities = await client._parse_message_text(caption or '', 'html')
    request = functions.messages.SendMediaRequest(
        peer=entity,
        media=video_media,
        message=caption_text,
        entities=caption_entities,
    )
    result = await client(request)
    return client._get_response_message(request, result, entity)

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
        cover_path = req.get('cover_path')
        thumb_path = req.get('thumb_path')
        if (req.get('embed_cover') or req.get('album_cover')) and cover_path and os.path.isfile(cover_path):
            try:
                thumb_path = cover_thumbnail(cover_path)
                print(
                    f"[cover] resource poster prepared as video thumbnail: {thumb_path}",
                    file=sys.stderr,
                    flush=True,
                )
            except Exception as exc:
                print(
                    f"[cover] resource poster thumbnail failed; "
                    f"using captured video frame: {exc}",
                    file=sys.stderr,
                    flush=True,
                )
            try:
                video_msg = await send_video_with_cover(
                    client,
                    entity,
                    cover_path,
                    thumb_path if thumb_path and os.path.isfile(thumb_path) else None,
                    req['file_path'],
                    req.get('caption', ''),
                )
            except Exception as exc:
                print(
                    f"[cover] native video cover failed; "
                    f"falling back to a standalone streamable video: {exc}",
                    file=sys.stderr,
                    flush=True,
                )
            else:
                print(json.dumps({
                    'message_id': video_msg.id,
                    'cover_attached': True,
                    'thumb_submitted': True,
                }))
                await client.disconnect()
                return
        kwargs = {
            'caption': req.get('caption', ''),
            'parse_mode': 'html',
            'supports_streaming': True,
            'force_document': False,
            'mime_type': 'video/mp4',
        }
        if thumb_path and os.path.isfile(thumb_path):
            kwargs['thumb'] = thumb_path
        attributes = video_attributes(req['file_path'])
        if attributes:
            kwargs['attributes'] = attributes
        progress = UploadProgress(req['file_path'])
        if fast_upload:
            print(
                f"[upload] parallel multipart started: "
                f"{os.path.basename(req['file_path'])} "
                f"({os.path.getsize(req['file_path']) / 1048576:.1f}MB)",
                file=sys.stderr,
                flush=True,
            )
            file_to_send = await fast_upload(
                client=client,
                file_path=req['file_path'],
                progress_callback=progress,
            )
        else:
            file_to_send = req['file_path']
            kwargs['progress_callback'] = progress
        msg = await client.send_file(entity, file_to_send, **kwargs)
        print(json.dumps({'message_id': msg.id, 'thumb_submitted': bool(kwargs.get('thumb'))})); await client.disconnect(); return
    msg = await client.send_message(
        entity,
        req['text'],
        parse_mode=None if req.get('plain_text') else (),
    )
    print(json.dumps({'message_id': msg.id}))
    await client.disconnect()

try:
    asyncio.run(main())
except Exception as exc:
    print(json.dumps({'error': str(exc)}))
    sys.exit(1)
