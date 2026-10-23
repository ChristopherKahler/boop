-- Web Push subscriptions (the installed web app or a browser) are devices with
-- platform 'web': the endpoint lives in device_token and its encryption keys
-- here, as JSON {"p256dh": "...", "auth": "..."}.
ALTER TABLE devices ADD COLUMN push_keys TEXT NOT NULL DEFAULT '';
