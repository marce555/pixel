#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import imaplib
import email
from email.header import decode_header
import json
import sys
import argparse
import re

def decode_mime_header(header_value):
    if not header_value:
        return ""
    decoded = decode_header(header_value)
    parts = []
    for text, encoding in decoded:
        if isinstance(text, bytes):
            try:
                parts.append(text.decode(encoding or 'utf-8', errors='ignore'))
            except Exception:
                parts.append(text.decode('latin1', errors='ignore'))
        else:
            parts.append(str(text))
    return "".join(parts)

def get_email_body(msg):
    body = ""
    if msg.is_multipart():
        for part in msg.walk():
            ctype = part.get_content_type()
            cdispo = str(part.get('Content-Disposition'))
            if ctype == 'text/plain' and 'attachment' not in cdispo:
                try:
                    payload = part.get_payload(decode=True)
                    charset = part.get_content_charset() or 'utf-8'
                    body = payload.decode(charset, errors='ignore')
                    break
                except Exception:
                    pass
        if not body:  # fallback to html if no plain text
            for part in msg.walk():
                ctype = part.get_content_type()
                if ctype == 'text/html':
                    try:
                        payload = part.get_payload(decode=True)
                        charset = part.get_content_charset() or 'utf-8'
                        body = payload.decode(charset, errors='ignore')
                        # Simple HTML stripping
                        body = re.sub(r'<[^>]+>', '', body)
                        break
                    except Exception:
                        pass
    else:
        try:
            payload = msg.get_payload(decode=True)
            charset = msg.get_content_charset() or 'utf-8'
            body = payload.decode(charset, errors='ignore')
        except Exception:
            pass
    return body.strip()

def main():
    parser = argparse.ArgumentParser(description="Fetch unseen Gmail emails.")
    parser.add_argument("--email", required=True)
    parser.add_argument("--password", required=True)
    parser.add_argument("--limit", type=int, default=5)
    args = parser.parse_args()

    try:
        mail = imaplib.IMAP4_SSL("imap.gmail.com")
        mail.login(args.email, args.password)
        mail.select("inbox")

        status, response = mail.search(None, "UNSEEN")
        if status != "OK":
            print(json.dumps({"error": "Failed to search unseen emails"}), file=sys.stderr)
            sys.exit(1)

        msg_ids = response[0].split()
        # Get only the last 'limit' emails
        msg_ids = msg_ids[-args.limit:]

        emails = []
        for msg_id in msg_ids:
            # Decode the msg_id to str
            msg_id_str = msg_id.decode('utf-8') if isinstance(msg_id, bytes) else str(msg_id)
            status, msg_data = mail.fetch(msg_id, "(BODY.PEEK[])")
            if status != "OK" or not msg_data or not msg_data[0]:
                continue
            
            raw_email = msg_data[0][1]
            msg = email.message_from_bytes(raw_email)

            subject = decode_mime_header(msg.get("Subject"))
            sender = decode_mime_header(msg.get("From"))
            date = msg.get("Date")
            msg_id_header = msg.get("Message-ID") or msg_id_str
            body = get_email_body(msg)

            # Limit body length to avoid overwhelming the LLM
            if len(body) > 1000:
                body = body[:1000] + "... (tronqué)"

            emails.append({
                "id": msg_id_header,
                "from": sender,
                "subject": subject,
                "date": date,
                "body": body
            })

        print(json.dumps(emails, indent=2))
        mail.logout()
    except Exception as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
