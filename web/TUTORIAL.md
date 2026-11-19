# Using gtd

This describes the tool as it is today (milestones M1 and M4, plus the notes screens).
In the app it is under the question mark in the top bar. Where something is planned but
not built yet, it says so. Screen and button names are the English ones; German is in
Settings.

## The idea in one minute

Your head is for having ideas, not for holding them. gtd gives every thought exactly
two moments:

1. **Capture**: get it out of your head in five seconds, without deciding anything.
2. **Clarify**: later, at your desk, decide once what it is and where it belongs.

After that a thought is one of four things: an **action** you can do, a **project**
(an outcome that needs more than one action), a **note** (something worth keeping),
or gone. You never look at a raw thought twice.

## The words

gtd uses a small vocabulary. Each word means one thing.

| Word | What it is | Example |
|---|---|---|
| **Capture** | A raw thought you got out of your head. It sits in the inbox until you decide what it is. | "tap in kitchen drips ask landlord" |
| **Action** | One physical thing you can do in one go. It starts with a verb. | "Call the plumber about the quote" |
| **Project** | An outcome that needs more than one action. It is phrased as the finished result, and it is done when that result is true. Projects can contain sub-projects. | "Kitchen renovated" |
| **Area** | A part of your life or work that you look after for as long as you have it. An area is never finished; it is where projects and actions belong. | "Home", "Health", "Finances" |
| **Note** | Something worth keeping: an idea, call notes, a design, a reference. Written in Markdown, with images and files. | "Call with the plumber, 19 Nov" |
| **Space** | Who can see it. You have one personal space; a shared space has members (for example a team, or you and your partner). | "Team", "Family" |

The difference between a project and an area: "Kitchen renovated" is a project, because
one day it is done. "Home" is an area, because it never is.

**States of an action**

| State | Meaning |
|---|---|
| **Next** | You could do it now. This is what Engage shows. |
| **Planned** | It waits for another action to be done first. gtd makes it "next" by itself when that happens. |
| **Waiting** | Someone else has to do something first. You are waiting for them. |
| **Someday** | Maybe, one day. Not a commitment. |
| **Done**, **Dropped**, **Trashed** | Finished; decided against; a mistake. Nothing is ever deleted for good. |

A project is **Active**, **Someday** or finished. An active project with no next action
is **stuck**, and gtd flags it. A note is **Unsorted** until you file it into a project
or area, then **Filed**, later perhaps **Archived**.

**The details of an action**

- **Context**: where or with what you can do it: Calls, Computer, Errands, Home, Office,
  Anywhere, or "Agenda" for things to raise with one person. Engage filters by it.
- **Not before**: hide it until a date. It comes back by itself.
- **Target**: when you would like it done. The normal kind of date.
- **Due**: a hard deadline with consequences. Use it rarely, so that it means something.
- **Energy** and **Time needed**: so Engage can offer what fits your state and your slot.
- **Waits for**: the actions that must be done first.

**Other words**

- **Thread**: the running commentary on any item, like the back of an index card.
- **Theme**: a project set to Someday that you use to collect ideas on one subject.
- **Inbox**: your captures, plus "From others" (for example an action of yours that just became unblocked).

## The daily loop

### 1. Capture, all day

Open gtd, type, press Enter. The field is empty again and nothing else happens. That
is deliberate: no counts, no suggestions, nothing to pull you in.

- One thought per capture. Half a sentence is enough: "tap in kitchen drips ask landlord".
- Shift+Enter adds a line. Pasting several lines asks whether that is one item or one per line.
- The camera and paperclip buttons attach a photo or file, with or without text.
- `c` jumps to Capture from anywhere on a keyboard.
- From the terminal: `gtd add "text"`.

**Already there?** While you type, gtd quietly lists up to four existing items that say
the same thing, also in other words if similarity is on. If you see last week's
version of this thought, press **Add to this instead**: your text goes into that
item's thread and nothing new is created. If you ignore the list, Enter captures as
always.

If you are sharing your screen, tick **Privacy mode** next to the field first. Nothing
from the past appears on the capture screen while it is on, and it stays on for that
device until you untick it.

Do not sort, title or judge while capturing. A bad capture that exists beats a good
one you were going to write later.

### 2. Clarify, once or twice a day

**Inbox → Process inbox.** You get one capture at a time, oldest first, and a short
series of questions. Each answer is one tap, or a number key (1 for the first option).

| Question | What it decides |
|---|---|
| What is it? | Fix the title so that it will make sense in a month, then say what it becomes: **Action** (one step; Enter), **Project** (more than one step; a new one, or added to an existing one), **Reference**, **Someday** or **Trash**. Alt+1 to Alt+5 press the buttons from the keyboard. |
| Next physical action? | One sentence, starting with a verb. "Call the plumber about the quote", not "Plumber". |
| Under 2 minutes? | Then do it now. A timer runs; press Done. |
| Who? | You, or someone you are waiting for. |
| When and where? | Context (Calls, Errands, …), not before, target, due, energy, time. All optional. |
| Anything after that? | For projects: follow-up steps, each waiting for the one before. |

Leaving in the middle is fine. Nothing is saved until the last step, and the capture
stays at the head of the inbox. There is no "skip": every capture gets a decision.

### 3. Engage, when you have time to work

**Engage** asks where you are, how much time you have and how much energy, and shows
at most seven actions that fit. Selecting a place shows only actions for that place
plus those marked "Anywhere".

- **Done** completes an action. If something was waiting for it, that becomes "next"
  and shows up in your inbox under "From others" as unblocked.
- **Defer** hides it until a date. **Not now** hides it until you leave the page.
- **Focus** shows one action alone, with a timer and a single capture field, so a
  stray thought costs you five seconds instead of your concentration.
- When you complete a project's last open action, gtd asks for the next one. A
  project without a next action is stuck, and stuck projects are flagged everywhere.

**Today** is the short list to look at first in the morning: what is due or has its
target today (or is overdue), and what came back today because you deferred it or
because it repeats. It ignores the three questions above.

The tabs **Waiting for** and **Due and targets** are for a quick look, not for working.

Keys: `c` capture, `e` Engage, `n` new note, `/` search, `j`/`k` move, `x` done,
`f` full screen, and number keys to answer a wizard question.

## Things you do again and again

For "take out the trash" or "exercise three times a week", open the action and set
**Repeat** in its details:

- **Every day / 2 days / week / …** counts from the day you did it. Do the bins on
  Wednesday instead of Tuesday and the next one is a week from Wednesday.
- **On weekdays…** lets you tick days, for example Monday, Wednesday and Friday.

When you mark a repeating action done, gtd makes the next one and hides it until its
day. There is only ever one of them open: if you skip a week, you find one reminder
waiting, not seven. To stop, drop the action or set Repeat back to "Does not repeat".

## Bringing in old notes

`gtd import obsidian <vault folder>` brings an Obsidian vault in. Every note arrives
as an **unsorted** note, with its links, images and attachments; nothing goes to the
inbox. Run it with `-dry-run` first to see the report without changing anything. It
is safe to run again: files that did not change are skipped.

Then open **Notes** and press **Sweep**. gtd shows the unsorted notes in groups
that are about the same thing, one group per screen. Untick what does not belong, and
decide once for the rest: into a project, a new project, a new theme (a someday
project), an area, or the archive. Skip a group you cannot decide on now.

## The weekly review

The review is what keeps the rest honest. Without it, lists fill with things that are
no longer true and you stop trusting them. Once a week, **Review** walks you through
everything, one decision per screen:

1. **Get clear.** The review does not start until the inbox is empty. Process it first.
2. **Unsorted notes.** File what has piled up.
3. **Projects**, one at a time: still alive? Keep, someday, done or drop. A project you
   keep must have a next action. If it has none, you add one right there, or you decide
   it is not a project you are doing.
4. **Stale actions**: "next" for two weeks without moving. Keep, defer, someday or drop.
5. **Waiting for** longer than a week: nudge (creates a follow-up action), keep or resolved.
6. **Dates**: what has slipped, what is implausible, what is coming up in two weeks.
7. **Someday / maybe**: ten of them each week, the ones you have not looked at longest.
   Activate, keep or drop.
8. **Areas**: an area with no active project is a part of your life you are not moving.

Each answer takes effect at once. You can stop anywhere and continue later, on any
device: the review remembers where you were. Set your review day in Settings, and a
banner reminds you on that day.

Items you deferred come back by themselves on their date, marked "back today" for
three days.

## Projects

A project is an outcome: "Kitchen renovated", not "Kitchen".
**Projects** lists them as a tree. Opening one shows its **plan**:

- its sub-projects, nested, each with done/total and a stuck or slipping flag;
- its actions, with what each one waits for;
- a field to add an action directly;
- its notes, and a **thread**.

Dependencies: drag action A onto action B and A waits for B. On a phone, open A and
use "Waits for" in its details. A waiting action is "planned" and never appears in
Engage until what it waits for is done.

The **thread** is for the running commentary of a project. Type "The plumber says the pipe has to be replaced, two days of work" and press Add. Each entry has **Make this an action**.
Use the thread instead of a new capture when you already know which project a thought
belongs to.

**Board** shows the same actions as columns (Inbox, Next, Planned, Waiting, Deferred,
Someday, Done) and can be narrowed to an area or a project. It is for an overview and
quick fixes. **Areas** are your areas of focus (Home, Health, Finances); an area
with no active project is flagged.

## Notes

A note is a Markdown document: headings, lists, tables, code, links, images, files.

**Notes** in the top bar is their home. It lists every note you can see, most recently
changed first, with the first lines of each. The tabs narrow it to Unsorted, Filed or
Archived; the filter box takes words; the project list narrows to one project.

- **Create one** with **New note** on that screen, with the `n` key from anywhere, from
  a project's plan (then it is filed in that project), or by answering "keep as
  reference" in Clarify.
- **The editor** shows what you write on the left and how it will look on the right
  (on a phone, a Preview button switches). It saves by itself a moment after you stop
  typing, and when you leave the page; Cmd+S saves at once. "Saved" next to the buttons
  tells you where you stand. **Done** closes the editor.
- **Link** another item by typing `[[` and picking it by title. Pasting an item's
  address from the browser does the same. The linked item shows "Linked from".
- **Images and files**: paste a screenshot straight from the clipboard, drop a file
  onto the text, or use the paperclip. Images appear inline.
- **New note from this** starts a note that links back to the one you are reading,
  for an idea that grows out of another.
- **Task lines**: any line written as `- [ ] call the plumber` is a candidate action.
- **Templates**: if the space has templates for notes, choosing one in Details on an
  empty note fills in its skeleton (for example "Agenda / Decisions").

The **Ideas** tab on the same screen shows what is parked: your themes (someday
projects) with the number of notes in each, and your someday actions, with Activate
and Drop next to each.

### If note-taking never clicked for you

Most note systems ask you to be a librarian: invent folders, add tags, link every note
to its neighbours, and review it all. Few people keep that up, and a vault of
disconnected notes is the usual result. gtd asks for much less.

**You only ever write three kinds of note:**

1. **A record of something that happened**: a call, a meeting, a test result. Write it
   while it happens, in the project it belongs to. Mark promises as task lines. Process
   the tasks afterwards. Then you are done with it.
2. **An idea that is not an action yet.** One thought per note, a few lines. File it
   into a theme. Do not polish it.
3. **A "where this stands" note** for a project or theme: what you believe, what you do
   not know, what happens next. This is the one note worth rewriting, and there is at
   most one per project.

**And you follow one rule: a note lives in a project or a theme.** That is all the
organizing there is. You do not need folders, tags or a habit of linking:

- *Finding it again* is the job of search, which matches by meaning, not only by words.
- *Seeing what belongs together* is the job of "Related" on each note and "Might
  belong here" on each project.
- *Not writing it twice* is the job of "Already there?" on the capture screen.

Keep notes short. A note that has grown past a couple of screens is usually three
notes, or a project that deserves its own "where this stands".

If you are unsure whether something is worth a note, capture it as one line and decide
in Clarify. Most of the time the answer is a next action or the trash, and that is fine.

### Call and meeting notes

1. Before the call, open the project and press **New note**. Type freely during the call.
2. Whenever someone (including you) commits to something, write it as a task line:
   `- [ ] send the plumber the photos`.
3. After the call, press **Process open tasks** on the note. Each task line comes up
   once: adjust the wording, choose the project (it can be a different one for each
   line), a context and dates, and press Create action, or Skip.
4. A processed line turns into a link to its action. The note now shows what was
   agreed and what became of it, and the actions show which call they came from.

## From a stream of half-formed ideas to a picture

This is the part people get wrong with any tool, so here is a method rather than a
feature list.

**The problem:** an immature idea is not an action and not yet a project. If you force
it into either, your lists fill with things you will never do. If you leave it in the
inbox, the inbox stops being trustworthy. If you drop it into a pile of loose notes,
you never see it again.

**The method: every idea gets a home, and homes are themes you keep coming back to.**

1. **Capture it raw.** One line. Do not develop it while you are busy with something else.

2. **In Clarify, be honest at "What is it?"** An immature idea is neither
   an Action nor a Project. Choose:
   - **Someday** if it is a possible thing to do ("learn to bake sourdough").
   - **Reference** if it is a thought, an observation or a question ("the heating bill keeps
     rising: is the insulation the real problem?").
   - **Trash** more often than feels comfortable. Most ideas are noise, and the good
     ones come back.

3. **File it into a theme, not into a pile.** When Clarify asks where a reference note
   belongs, pick a project. If no project fits and this is the second or third idea
   on the same subject, type a name under "Or a new project": that creates the theme.
   Then open that project and set it to **Someday**, so that it does not count as a
   commitment and is not flagged as stuck. You now have a container that costs nothing.

4. **Let the theme's plan page be the picture.** It lists every note you filed there,
   and its thread holds the small additions. When you have a new thought about an
   existing theme, do not capture it: open the theme and add a line to its thread.

5. **Write one summary note per theme when it has a handful of ideas.** Create a note
   in the project called "Where this stands". Link the idea notes with `[[`, and write
   three things: what you now believe, what you still do not know, and what would have
   to be true for this to be worth doing. This is where separate ideas become one
   picture, and it is work only you can do today. (From M7 the tool drafts it; see below.)

6. **Promote it when it is ready.** When the summary note ends in "so I should…",
   write those as task lines, press **Process open tasks**, and set the project to
   Active. From then on it lives in Engage like everything else.

7. **Do the weekly review.** Its Someday step brings ideas back to you ten at a time, and
   you can also look at Notes → Ideas whenever you like. It shows your themes with their notes and your
   someday actions in one place. Activate, keep or drop. Ideas that survive three
   looks without growing can go. Then open the Unsorted tab and file what is there.

**What is missing today, plainly:**

- Unsorted notes are listed under Notes → Unsorted, but nothing groups them for you
  yet. Filing each one into a project or area is by hand until M5.
- With embeddings switched on (M4, see "Similarity" below) the tool notices that a new
  idea resembles older ones. It does not yet group loose notes (M5) or draft the
  summary (M7).

## Similarity

If the server has an embedding model configured, gtd compares items by meaning, in
German and English alike, and only ever within the spaces you are a member of.

- **Clarify** shows up to three existing items that resemble the capture, with a
  score. If one is the same thought, **Duplicate of…** appends the capture to that
  item instead of creating another one. Where you choose a project, the nearest
  projects are offered as **Suggested** chips. Nothing is chosen for you.
- **Search** lists exact matches first, then "Similar in meaning": items that say the
  same with other words.
- An item's page lists **Related** items. A project's plan lists loose notes that
  **might belong here**, with one tap to file them.
- Settings shows the state: ready, building or offline. While the index is building or
  the model does not answer, search says that it is a text match only.

A new capture is compared a second or two after you made it. If you clarify it
faster than that, you get no suggestions for it.

## Settings

Language, theme (Light, Dark, System), your own contexts (usable in your personal
space), and templates. A template adds structured fields to notes, projects or
actions of a space, for example a "stage" on job applications that then shows as a
badge and can filter the board.

## Backups

`gtd snapshot create` takes a consistent snapshot; `gtd export <dir>` writes everything
as readable Markdown files, which you can keep in a git repository. `gtd import -restore`
rebuilds a server from such an export.
