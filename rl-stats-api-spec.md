# Rocket League Stats API — Full Specification Extract

## Transport

- **Protocol:** WebSocket
- **Host:** localhost (player's machine)
- **Default Port:** 49123 (configurable)
- **Data Format:** JSON
- **Direction:** Server → Client (broadcast only)
- **Lifecycle:** Socket is active while a match is in progress

---

## Configuration

File: `<Install Dir>\TAGame\Config\DefaultStatsAPI.ini`
Must be edited **before** client launch (requires restart for changes).

| Setting        | Type  | Default       | Description                                                                 |
|----------------|-------|---------------|-----------------------------------------------------------------------------|
| PacketSendRate | float | 0 (disabled)  | UpdateState packets per second. Must be >0 to enable the socket. Max 120.   |
| Port           | int   | 49123         | Local port the socket listens on.                                           |

---

## Message Envelope

Every message uses this wrapper:

```json
{
  "Event": "<string: event name>",
  "Data":  { }
}
```

---

## Field Visibility Tags

- **CONDITIONAL** — field is only present when relevant (check with pointer/omitempty in Go).
- **SPECTATOR** — field is only present if the client is spectating or on the same team as the player.

---

## Shared / Reusable Types

### PlayerIdentifier (compact player reference used in many events)

| Field    | Type   | Description                              |
|----------|--------|------------------------------------------|
| Name     | string | Display name.                            |
| Shortcut | int    | Spectator shortcut number.               |
| TeamNum  | int    | Team index (0 = Blue, 1 = Orange).       |

Used in: BallHit.Players[], CrossbarHit.BallLastTouch.Player, GoalScored.Scorer,
GoalScored.Assister (CONDITIONAL), GoalScored.BallLastTouch.Player,
StatfeedEvent.MainTarget, StatfeedEvent.SecondaryTarget (CONDITIONAL),
UpdateState.Game.Target (CONDITIONAL), UpdateState.Player.Attacker (CONDITIONAL).

### Vector3

| Field | Type  | Description       |
|-------|-------|-------------------|
| X     | float | World X position. |
| Y     | float | World Y position. |
| Z     | float | World Z position. |

Used in: BallHit.Ball.Location, CrossbarHit.BallLocation, GoalScored.ImpactLocation.

### BallLastTouch

| Field  | Type             | Description                                  |
|--------|------------------|----------------------------------------------|
| Player | PlayerIdentifier | The player who made the last touch.          |
| Speed  | float            | Speed of the ball resulting from this touch.  |

Used in: CrossbarHit.BallLastTouch, GoalScored.BallLastTouch.

---

## Events

### 1. UpdateState (Tick)

**Trigger:** Sent at the configured PacketSendRate (X times per second).
Event data is always emitted on the same tick as the event, regardless of PacketSendRate.

#### Top-Level Data

| Field     | Type     | Description                                    |
|-----------|----------|------------------------------------------------|
| MatchGuid | string   | Only set for online or LAN matches.            |
| Players   | array    | One entry per player. See Player below.        |
| Game      | object   | Match metadata. See Game below.                |

#### Player Object (UpdateState.Players[])

| Field         | Type   | Visibility  | Description                                                                 |
|---------------|--------|-------------|-----------------------------------------------------------------------------|
| Name          | string |             | Display name.                                                               |
| PrimaryId     | string |             | Platform ID: "Platform\|Uid\|Splitscreen" (e.g. "Steam\|123\|0", "Epic\|456\|0"). |
| Shortcut      | int    |             | Spectator shortcut number.                                                  |
| TeamNum       | int    |             | Team index (0 = Blue, 1 = Orange).                                          |
| Score         | int    |             | Total match score.                                                          |
| Goals         | int    |             | Goals scored this match.                                                    |
| Shots         | int    |             | Shot attempts this match.                                                   |
| Assists       | int    |             | Assists earned this match.                                                  |
| Saves         | int    |             | Saves made this match.                                                      |
| Touches       | int    |             | Total ball touches.                                                         |
| CarTouches    | int    |             | Touches by the car body (not ball).                                         |
| Demos         | int    |             | Demolitions inflicted.                                                      |
| bHasCar       | bool   | SPECTATOR   | True if the player currently has a vehicle.                                 |
| Speed         | float  | SPECTATOR   | Vehicle speed in Unreal Units/second.                                       |
| Boost         | int    | SPECTATOR   | Boost amount 0–100.                                                         |
| bBoosting     | bool   | SPECTATOR   | True if the player is currently boosting.                                   |
| bOnGround     | bool   | SPECTATOR   | True if at least 3 wheels touch the world.                                  |
| bOnWall       | bool   | SPECTATOR   | True if the vehicle is on a wall.                                           |
| bPowersliding | bool   | SPECTATOR   | True if the player is holding handbrake.                                    |
| bDemolished   | bool   | SPECTATOR   | True if the vehicle is currently destroyed.                                 |
| bSupersonic   | bool   | SPECTATOR   | True if the vehicle is at supersonic speed.                                 |
| Attacker      | object | CONDITIONAL | PlayerIdentifier of demolisher. Present only when bDemolished is true.      |

#### Game Object (UpdateState.Game)

| Field      | Type   | Visibility  | Description                                                                |
|------------|--------|-------------|----------------------------------------------------------------------------|
| Teams      | array  |             | One entry per team, ordered by TeamNum. See Team below.                    |
| TimeSeconds| int    |             | Seconds remaining in the match.                                            |
| bOvertime  | bool   |             | True if the match is in overtime.                                          |
| Ball       | object |             | Current ball state. See Ball below.                                        |
| bReplay    | bool   |             | True if a goal replay or history replay is active.                         |
| bHasWinner | bool   |             | True if a team has won.                                                    |
| Winner     | string |             | Name of the winning team. Empty string if no winner yet.                   |
| Arena      | string |             | Asset name of the current map (e.g. "Stadium_P").                          |
| bHasTarget | bool   |             | True if the client is currently viewing a specific vehicle.                |
| Target     | object | CONDITIONAL | PlayerIdentifier of viewed player. Fields are "" or 0 if no target.       |
| Frame      | int    | CONDITIONAL | Current frame number if a replay is active.                                |
| Elapsed    | float  | CONDITIONAL | Seconds elapsed since game start if a replay is active.                    |

#### Team Object (UpdateState.Game.Teams[])

| Field          | Type   | Description                                          |
|----------------|--------|------------------------------------------------------|
| Name           | string | Team name.                                           |
| TeamNum        | int    | Team index.                                          |
| Score          | int    | Team goal count.                                     |
| ColorPrimary   | string | Hex color code (no #) for primary color.             |
| ColorSecondary | string | Hex color code for secondary color.                  |

#### Ball Object (UpdateState.Game.Ball)

| Field   | Type  | Description                                                        |
|---------|-------|--------------------------------------------------------------------|
| Speed   | float | Current ball speed in Unreal Units/second.                         |
| TeamNum | int   | Index of the last team to touch the ball. 255 = not yet touched.   |

---

### 2. BallHit

**Trigger:** Sent one frame after the ball is hit.

| Field     | Type   | Description                                  |
|-----------|--------|----------------------------------------------|
| MatchGuid | string | Only set for online or LAN matches.          |
| Players   | array  | PlayerIdentifier[] — players that hit the ball that frame. |
| Ball      | object | See below.                                   |

#### BallHit.Ball

| Field        | Type    | Description                                  |
|--------------|---------|----------------------------------------------|
| PreHitSpeed  | float   | Ball speed before the hit (UU/s).            |
| PostHitSpeed | float   | Ball speed after the hit (UU/s).             |
| Location     | Vector3 | World position of the ball at impact.        |

---

### 3. ClockUpdatedSeconds

**Trigger:** Sent when the in-game clock changes.

| Field       | Type   | Description                          |
|-------------|--------|--------------------------------------|
| MatchGuid   | string | Only set for online or LAN matches.  |
| TimeSeconds | int    | Seconds remaining in the match.      |
| bOvertime   | bool   | True if the game is in overtime.     |

---

### 4. CountdownBegin

**Trigger:** Sent at the start of each round when the countdown starts.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 5. CrossbarHit

**Trigger:** Sent when the ball hits a crossbar.

| Field         | Type          | Description                                              |
|---------------|---------------|----------------------------------------------------------|
| MatchGuid     | string        | Only set for online or LAN matches.                      |
| BallSpeed     | float         | Ball speed on impact.                                    |
| ImpactForce   | float         | Impact force relative to the crossbar normal.            |
| BallLastTouch | BallLastTouch | Last touch before the crossbar hit.                      |
| BallLocation  | Vector3       | World position of the ball at impact.                    |

---

### 6. GoalReplayEnd

**Trigger:** Sent when a goal replay ends.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 7. GoalReplayStart

**Trigger:** Sent when a goal replay starts.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 8. GoalReplayWillEnd

**Trigger:** Sent when the ball explodes during a goal replay. Does NOT fire if the replay is skipped.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 9. GoalScored

**Trigger:** Sent when a goal is scored.

| Field          | Type             | Visibility  | Description                                          |
|----------------|------------------|-------------|------------------------------------------------------|
| MatchGuid      | string           |             | Only set for online or LAN matches.                  |
| GoalSpeed      | float            |             | Ball speed (UU/s) when it crossed the goal line.     |
| GoalTime       | float            |             | Length of the previous round in seconds.              |
| ImpactLocation | Vector3          |             | World position of the ball when the goal was scored.  |
| Scorer         | PlayerIdentifier |             | Player who scored.                                   |
| Assister       | PlayerIdentifier | CONDITIONAL | Player who assisted. Present only when assist recorded. |
| BallLastTouch  | BallLastTouch    |             | Last touch of the ball before the goal.              |

---

### 10. MatchCreated

**Trigger:** Sent when all teams are created and replicated.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 11. MatchInitialized

**Trigger:** Sent when the first countdown starts.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 12. MatchDestroyed

**Trigger:** Sent when leaving the game.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 13. MatchEnded

**Trigger:** Sent when the match ends and a winner is chosen.

| Field         | Type   | Description                         |
|---------------|--------|-------------------------------------|
| MatchGuid     | string | Only set for online or LAN matches. |
| WinnerTeamNum | int    | Team index of the winning team.     |

---

### 14. MatchPaused

**Trigger:** Sent when the game is paused by a match admin.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 15. MatchUnpaused

**Trigger:** Sent when the game is unpaused by a match admin.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 16. PodiumStart

**Trigger:** Sent when the game enters the podium state after the match ends.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 17. ReplayCreated

**Trigger:** Sent when a replay is initialized. Pertains to Match History replays only, NOT goal replays.

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 18. RoundStarted

**Trigger:** Sent when the game enters the active state (after countdown finishes).

| Field     | Type   | Description                         |
|-----------|--------|-------------------------------------|
| MatchGuid | string | Only set for online or LAN matches. |

---

### 19. StatfeedEvent

**Trigger:** Sent when someone earns a stat.

| Field           | Type             | Visibility  | Description                                                      |
|-----------------|------------------|-------------|------------------------------------------------------------------|
| MatchGuid       | string           |             | Only set for online or LAN matches.                              |
| EventName       | string           |             | Asset name of the StatEvent (e.g. "Demolish", "Save").           |
| Type            | string           |             | Localized display label for the stat (e.g. "Demolition").        |
| MainTarget      | PlayerIdentifier |             | Player who earned the stat.                                      |
| SecondaryTarget | PlayerIdentifier | CONDITIONAL | Other player involved (e.g. demolished player). Same shape as MainTarget. |

---

## Complete Event Name Registry

These are all valid values for the top-level `"Event"` field:

1. `UpdateState`
2. `BallHit`
3. `ClockUpdatedSeconds`
4. `CountdownBegin`
5. `CrossbarHit`
6. `GoalReplayEnd`
7. `GoalReplayStart`
8. `GoalReplayWillEnd`
9. `GoalScored`
10. `MatchCreated`
11. `MatchInitialized`
12. `MatchDestroyed`
13. `MatchEnded`
14. `MatchPaused`
15. `MatchUnpaused`
16. `PodiumStart`
17. `ReplayCreated`
18. `RoundStarted`
19. `StatfeedEvent`

---

## Go SDK Type Mapping Summary

| API Type | Go Type                  | Notes                                        |
|----------|--------------------------|----------------------------------------------|
| string   | string                   |                                              |
| int      | int                      |                                              |
| float    | float64                  |                                              |
| bool     | bool                     | JSON keys use `b` prefix (bOvertime, etc.)   |
| array    | []T                      |                                              |
| object   | struct                   |                                              |
| vector   | Vector3 struct           | X, Y, Z float64                              |
| CONDITIONAL fields | *T (pointer) | Use `omitempty` JSON tag                     |
| SPECTATOR fields   | *T (pointer) | Use `omitempty` JSON tag; absent when not spectating same team |

---

## Go Struct Hierarchy (quick reference)

```
Message
├── Event  string
├── Data   json.RawMessage  (decode per Event)

Vector3 { X, Y, Z float64 }

PlayerIdentifier { Name string, Shortcut int, TeamNum int }

BallLastTouch { Player PlayerIdentifier, Speed float64 }

UpdateStateData
├── MatchGuid string
├── Players []UpdateStatePlayer
│   ├── Name, PrimaryId string
│   ├── Shortcut, TeamNum, Score, Goals, Shots, Assists, Saves, Touches, CarTouches, Demos int
│   ├── HasCar, Boosting, OnGround, OnWall, Powersliding, Demolished, Supersonic *bool   [SPECTATOR]
│   ├── Speed *float64  [SPECTATOR]
│   ├── Boost *int      [SPECTATOR]
│   └── Attacker *PlayerIdentifier  [CONDITIONAL]
├── Game
│   ├── Teams []Team { Name string, TeamNum int, Score int, ColorPrimary string, ColorSecondary string }
│   ├── TimeSeconds int
│   ├── Overtime bool
│   ├── Ball { Speed float64, TeamNum int }
│   ├── Replay bool
│   ├── HasWinner bool
│   ├── Winner string
│   ├── Arena string
│   ├── HasTarget bool
│   ├── Target *PlayerIdentifier  [CONDITIONAL]
│   ├── Frame *int      [CONDITIONAL]
│   └── Elapsed *float64 [CONDITIONAL]

BallHitData
├── MatchGuid string
├── Players []PlayerIdentifier
├── Ball { PreHitSpeed float64, PostHitSpeed float64, Location Vector3 }

ClockUpdatedSecondsData { MatchGuid string, TimeSeconds int, Overtime bool }

CountdownBeginData { MatchGuid string }

CrossbarHitData
├── MatchGuid string
├── BallSpeed float64
├── ImpactForce float64
├── BallLastTouch BallLastTouch
├── BallLocation Vector3

GoalReplayEndData      { MatchGuid string }
GoalReplayStartData    { MatchGuid string }
GoalReplayWillEndData  { MatchGuid string }

GoalScoredData
├── MatchGuid string
├── GoalSpeed float64
├── GoalTime float64
├── ImpactLocation Vector3
├── Scorer PlayerIdentifier
├── Assister *PlayerIdentifier  [CONDITIONAL]
├── BallLastTouch BallLastTouch

MatchCreatedData       { MatchGuid string }
MatchInitializedData   { MatchGuid string }
MatchDestroyedData     { MatchGuid string }

MatchEndedData { MatchGuid string, WinnerTeamNum int }

MatchPausedData        { MatchGuid string }
MatchUnpausedData      { MatchGuid string }
PodiumStartData        { MatchGuid string }
ReplayCreatedData      { MatchGuid string }
RoundStartedData       { MatchGuid string }

StatfeedEventData
├── MatchGuid string
├── EventName string
├── Type string
├── MainTarget PlayerIdentifier
├── SecondaryTarget *PlayerIdentifier  [CONDITIONAL]
```

---

## Important Behavioral Notes

1. **MatchGuid** is present on every event's Data but is only populated for online/LAN matches (empty for local/exhibition).
2. **UpdateState** is the only periodic (tick) event. All other events are fired exactly once when the triggering condition occurs.
3. Event data is emitted on the **same tick** as the event, independent of PacketSendRate.
4. **Ball.TeamNum = 255** means the ball has not been touched yet.
5. **Target** fields (Name/Shortcut/TeamNum) are empty string / 0 when the client has no spectator target, even when `bHasTarget` might technically be true.
6. **GoalReplayWillEnd** does NOT fire if the replay is skipped by players.
7. **ReplayCreated** pertains to Match History replays only — not goal replays.
8. **PrimaryId** format is `Platform|Uid|Splitscreen` — known platforms include `Steam`, `Epic`.
