import asyncio, json, os, sys
from telethon import TelegramClient

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
    if req.get('action') == 'upload_video':
        msg = await client.send_file(int(req['chat_id']), req['file_path'], caption=req.get('caption', ''), parse_mode='html', supports_streaming=True)
        print(json.dumps({'message_id': msg.id})); await client.disconnect(); return
    msg = await client.send_message(int(req['chat_id']), req['text'])
    print(json.dumps({'message_id': msg.id}))
    await client.disconnect()

try:
    asyncio.run(main())
except Exception as exc:
    print(json.dumps({'error': str(exc)}))
    sys.exit(1)
