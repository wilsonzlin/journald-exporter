# journald-exporter

Lightweight single-binary Linux program for exporting [journald](https://www.freedesktop.org/software/systemd/man/systemd-journald.service.html) log entries to these services:

- [AWS CloudWatch Logs](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/WhatIsCloudWatchLogs.html)
- [Oracle Cloud Logging](https://docs.oracle.com/en-us/iaas/Content/Logging/Concepts/loggingoverview.htm)

## Get it now

### AWS

[Linux x64](https://static.wilsonl.in/journald-exporter/0.3.3/linux/x86_64/journald-exporter-aws) | [Linux ARM64](https://static.wilsonl.in/journald-exporter/0.3.3/linux/aarch64/journald-exporter-aws)

### Oracle Cloud

[Linux x64](https://static.wilsonl.in/journald-exporter/0.3.3/linux/x86_64/journald-exporter-oci) | [Linux ARM64](https://static.wilsonl.in/journald-exporter/0.3.3/linux/aarch64/journald-exporter-oci)

## Setup

Simply download and run:

```bash
curl -fLSs <insert download URL here> --output /usr/bin/journald-exporter
chmod +x /usr/bin/journald-exporter
mkdir /persistent_state_dir_for_this_exporter
# For AWS
/usr/bin/journald-exporter \
  --log-group /my/log/group \
  --log-stream my_log_stream \
  --state-dir /persistent_state_dir_for_this_exporter
# For Oracle Cloud
/usr/bin/journald-exporter \
  --log-ocid ocid.aaasdfasdfasdf \
  --instance-ocid ocid.aaasdfasdfasdfd \
  --state-dir /persistent_state_dir_for_this_exporter
```

All exporters require a persistent directory to hold state. This directory must be only used by a single exporter, and its contents must not modified or removed between system reboots.

While the process is running, journald entries will be streamed and uploaded to the cloud service as soon as possible. The exporter will batch as many entries as possible per request to reduce costs and delays.

Only warning and error messages will be omitted; nothing is shown when idle, transferring, or successful.

The process can be safely killed/interrupted at any time; restarting the process with the same arguments will resume where it left off and not cause any entries to be lost. However, it's strongly recommended to configure journald itself to store all logs persistently and rotate sparingly, to avoid journald from losing messages (e.g. when rebooting or out of disk space). See the man page for [journald.conf(5)](https://www.freedesktop.org/software/systemd/man/journald.conf.html) for more details.

## Background service

To have the exporter run in the background and optionally automatically start when the system starts, create a systemd service for it by creating this file at `/etc/systemd/system/journald-exporter.service`:

```
[Unit]
Description=journald-exporter
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/bin/journald-exporter --log-group /my/log/group --log-stream my_log_stream --state-dir /persistent_state_dir_for_this_exporter
SyslogLevelPrefix=no
StandardOutput=journal
StandardError=journal
Restart=no

[Install]
WantedBy=multi-user.target
```

Once the file has been created, run the following commands as root:

```bash
systemctl daemon-reload
systemctl --now enable journald-exporter
```
