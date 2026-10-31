## Security

**Read [SECURITY.md](../SECURITY.md) before pointing agents you don't trust at one
daemon.** The trust boundary is the machine: every agent shares one coordination
secret, so Dibs protects you from other users and from the network, and
raises (but cannot wall off) what one of your own agents can learn about
another. Run a second daemon for anything you do not trust.
