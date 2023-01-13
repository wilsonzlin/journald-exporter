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
