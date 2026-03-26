# Paradise Bay server

In 2015, King/Z2 made the "Paradise Bay" game which they discontinued in 2018, with servers closing. At the time I was playing this game. I wanted to re-play it but couldn't as the servers closed.

Fortunately, the servers URLs are just in an array in the `game-info.json` file, which is stored in the Appx package (on Windows).

![](./screenshot.png)
The game running on Windows 11 25H2 and being debugged on Visual Studio 2022.

## Patching

### Downloading the `.appx`

1. Go to https://store.rg-adguard.net/
2. Filter by ProductId, search for `9nblggh5l706`
3. Download `king.com.ParadiseBay_3.9.0.0_x86__kgqvnymyfvs32.appx` (the last file)

### Modifying files

1. Extract the appx using 7-Zip (WinRAR probably works)
2. Open `game-info.json`, search for `"Server List":`, and replace `"http://tk1-win.z2live.com/"` with `"http://localhost:3300"` (keep the quotes)

### Installing the game

1. Enable "Developer Mode":
   - **Windows 10:** idk 
   - **Windows 11:** System > For developers > Check "Developer Mode"
2. Run `install.bat`
