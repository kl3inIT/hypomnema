# The phone

An Android phone agents use as they use the shared browser (issue #417): an emulator with the Play Store, on a node of
its own. The owner signs in once, every agent sees and drives it, and the owner watches and takes over from the web.
What the code does is in [ARCHITECTURE.md](../../ARCHITECTURE.md) ("A phone, on a node that holds one").

## Make it

On AWS, a machine made with `--device android`:
[the AWS machine guide, section 5](aws.md#5-a-machine-that-holds-an-android-emulator) gives the command, the sizes
that can run an emulator, what its first boot installs and what it costs. The node downloads the emulator and the
Android 14 Play image (about 2 GB) at the phone's first start:

```sh
hypomnema device start phone    # installs at the first start, then boots; minutes
hypomnema device status         # absent, installing, starting, running, stopped or failed, with what is installed
hypomnema device stop phone     # its disk, with what is signed in on it, stays
```

## Sign in

Open a chat, choose **Phone** in the pane's **+** menu, take control and sign in to Google, the Play Store and the
apps there. Install apps from the Play Store the same way. An agent never signs in to an account; the sign-ins stay
on the phone's disk when it stops, and when its machine stops.

## Who uses it

Every agent that has the shared browser's tools has the phone's (`device_screen`, `device_tap`, `device_type` and the
others; skill `android-device`). One agent's call runs at a time. While the owner holds control in the view, agents'
calls are answered that the phone is paused.

## When it runs

| What | When | Started again by |
|---|---|---|
| The phone (the emulator) | Stopped 15 minutes after its last use: a start, an agent's call, or a view while it is open | the next call, view or `device start` |
| Its machine | Stopped 30 minutes after that, through the path of `machines stop`: the phone stopped, no call running and no view open for 30 minutes | the next call or view, which waits for the node (about a minute) |

A call that finds the machine stopped waits up to 2 minutes for it, then says the phone is waking and to try again; a
view waits for as long as it is open. The machine is not stopped while its node runs a Dispatch; the stop is asked
again each minute. A node installed by hand, not made by a provider, is never stopped.

Running, the machine costs about $0.39 an hour; stopped, about $3.84 a month for its disk (Singapore, on demand).
`hypomnema machines stop phone` stops it at once; `machines start phone` starts it.
