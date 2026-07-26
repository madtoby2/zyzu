import asyncio, json, os, sys
from telethon import TelegramClient

async def main():
    req = json.loads(sys.stdin.read())
    client = TelegramClient(os.environ.get('ZYZU_TELETHON_SESSION', 'telethon'), int(os.environ.get('ZYZU_API_ID', '11535358')), os.environ.get('ZYZU_API_HASH', '33d372962fadb01df47e6ceed4e33cd6'))
    await client.connect()
    if not await client.is_user_authorized():
        raise RuntimeError('telethon session is not authorized')
    msg = await client.send_message(int(req['chat_id']), req['text'])
    print(json.dumps({'message_id': msg.id}))
    await client.disconnect()

try:
    asyncio.run(main())
except Exception as exc:
    print(json.dumps({'error': str(exc)}))
    sys.exit(1)
