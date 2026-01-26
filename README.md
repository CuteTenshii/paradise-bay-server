# Paradise Bay server

In 2015, King/Z2 made the "Paradise Bay" game which they discontinued in 2018, with servers closing. At the time I was playing this game. I wanted to re-play it but couldn't as the servers closed.

Fortunately, the servers URLs are just in an array in the `game-info.json` file, which is stored in the Appx package (on Windows).

## Patching

### Downloading the `.appx`

1. Go to https://store.rg-adguard.net/
2. Filter by ProductId, search for `9nblggh5l706`
3. Download `king.com.ParadiseBay_3.9.0.0_x86__kgqvnymyfvs32.appx` (the last file)

### Modifying files

1. Extract the appx using 7-Zip (WinRAR probably works)
2. Delete `AppxSignature.p7x` (required otherwise Windows won't install the package) 
3. Open `game-info.json`, search for `"Server List":`
4. Before `"http://tk1-win.z2live.com/"`, add `"http://localhost:8080"` (with the quotes)

### Installing the game

1. Enable "Developer Mode":
   - **Windows 10:** idk 
   - **Windows 11:** System > For developers > Check "Developer Mode"
2. Open PowerShell, cd to the extracted appx and run this:
   ```shell
   Add-AppxPackage -Register ".\AppxManifest.xml"
   ```
   which registers the Appx package. If everything went well, you should see the game in your start menu

## Progress

When sending the `connect` payload back to the server, you need to send some info. I'm searching for this stuff across the code in BN.

To this day I'm stuck with this issue:
```shell
Z2PCodeError: ZPSocketChannel::onConnectResponse. Session empty! {
}
onecore\base\AppModel\Runtime\Src\PackagePath.hpp(144)\kernelbase.dll!7669646E: (caller: 76695FF9) ReturnHr(20) tid(1304) 80073D5B The package does not have a mutable directory.
SID    FBD5, 367.255s: Z2PCodeError: ZPSocketChannel::onConnectResponse. Session empty! {
}
```

I think some fields are missing in the payload but I'm struggling finding them.