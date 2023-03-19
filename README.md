# maid

Bot to help with room moderation

![2023-03-19_19-20](https://user-images.githubusercontent.com/52239427/226189626-b7c005ef-f2c6-4624-bbec-a35457420ea1.png)

## Requirements

- [Matrix olm](https://gitlab.matrix.org/matrix-org/olm)
- Go

## Usage

1. Copy and modify env file
  ```bash
  cp scripts/env.bash.example scripts/env.bash
  ```
2. Start the bot
  ```bash
  scripts/run.bash
  ```
3. Invite to any room and give permission to send, redact messages and kick users
  
### Running on server:

```conf
[Unit]
Description=Maid (Matrix Bot)
After=syslog.target
After=network.target

[Service]
RestartSec=2s
Type=simple
User=filleron
Group=filleron
WorkingDirectory=/home/filleron/projects/maid/
ExecStart=/home/filleron/projects/maid/scripts/run.bash
Restart=always
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```
