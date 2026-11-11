// One instance belongs to one document. Nothing here reads or writes storage.
const sample = [
  ['Alex Chen', 'intermediate'], ['Jamie Rivera', 'advanced'],
  ['Drew Patel', 'intermediate'], ['Sam Brooks', 'beginner'],
  ['Jules Park', 'beginner'], ['Morgan Lee', 'advanced'],
  ['Robin Diaz', 'beginner'], ['Taylor Reed', 'intermediate'],
];

export class PracticeEvent {
  constructor(title, courts, withSample, rules, now = Date.now) {
    this.title = title.trim();
    if (!this.title || this.title.length > 80 || !Number.isInteger(courts) || courts < 1 || courts > 4)
      throw new Error('Enter an event name and choose one to four courts.');
    this.rules = rules;
    this.now = now;
    this.phase = 'Setup';
    this.players = [];
    this.courts = Array(courts).fill(null);
    this.history = [];
    this.nextMatch = 1;
    this.meetings = new Map();
    if (withSample) for (const [name, band] of sample) this.add(name, band);
  }

  add(name, band = 'beginner') {
    name = name.trim();
    if (this.phase === 'Finished' || !name || name.length > 60 || this.players.length >= 100)
      throw new Error('Use a name of 1–60 characters on an open roster of fewer than 100.');
    if (this.players.some(p => p.name.toLowerCase() === name.toLowerCase()))
      throw new Error('That player is already on this roster.');
    this.players.push({id: this.players.length + 1, name, rating: this.rules[band] ?? this.rules.beginner,
      state: 'Resting', checkedAt: null, readyAt: null, wins: 0, games: 0});
  }
  toggle(p) {
    if (this.phase === 'Finished' || p.state === 'Playing') return;
    if (p.state === 'Ready') { p.state = 'Resting'; return; }
    p.state = 'Ready';
    p.readyAt = this.now();
    p.checkedAt ??= p.readyAt;
  }
  leave(p) {
    if (this.phase !== 'Finished' && p.checkedAt != null && p.state !== 'Playing') p.state = 'Left';
  }
  checkAll() {
    for (const p of this.players) if (p.state === 'Resting') this.toggle(p);
  }
  start() {
    if (this.phase !== 'Setup') return;
    this.phase = 'Live';
    this.started = this.now();
  }
  queue() {
    return this.players.filter(p => p.state === 'Ready').sort((a,b) => a.readyAt - b.readyAt || a.id - b.id);
  }
  pairKey(a,b) { return a.id < b.id ? a.id + ':' + b.id : b.id + ':' + a.id; }
  assign(court) {
    const queue = this.queue();
    if (this.phase !== 'Live' || !(court in this.courts) || this.courts[court] || queue.length < 4) return false;
    // Same seven-minute wait rule and three possible doubles splits as the club matcher.
    const picked = [];
    while (picked.length < 4) {
      const cutoff = queue[0].readyAt + this.rules.window;
      let best = 0, fewest = Infinity;
      for (let i = 0; i < queue.length && queue[i].readyAt <= cutoff; i++) {
        const shared = picked.reduce((n,p) => n + (this.meetings.get(this.pairKey(queue[i],p))?.shared ?? 0), 0);
        if (shared < fewest) { best = i; fewest = shared; }
      }
      picked.push(queue.splice(best, 1)[0]);
    }
    let teams, gap = Infinity, repeats = Infinity;
    for (const split of [[0,1,2,3],[0,2,1,3],[0,3,1,2]]) {
      const [a,b,c,d] = split.map(i => picked[i]);
      const diff = Math.abs(a.rating + b.rating - c.rating - d.rating);
      const partners = (this.meetings.get(this.pairKey(a,b))?.partners ?? 0) + (this.meetings.get(this.pairKey(c,d))?.partners ?? 0);
      if (diff < gap || (diff === gap && partners < repeats)) {
        teams = [[a,b],[c,d]]; gap = diff; repeats = partners;
      }
    }
    for (const p of picked) p.state = 'Playing';
    this.courts[court] = {id:this.nextMatch++, court, teams};
    return true;
  }
  record(court, winner) {
    const match = this.courts[court];
    if (!match || ![0,1,2].includes(winner)) return false;
    if (winner) {
      const means = match.teams.map(([a,b]) => (a.rating + b.rating)/2);
      const expected = 1/(1 + 10 ** ((means[1] - means[0])/400));
      const delta = this.rules.k * ((winner === 1 ? 1 : 0) - expected);
      match.teams.forEach((team, i) => team.forEach(p => {
        p.rating += i === 0 ? delta : -delta;
        p.games++;
        if (winner === i + 1) p.wins++;
      }));
      const all = match.teams.flat();
      for (let i = 0; i < 4; i++) for (let j = i+1; j < 4; j++) {
        const key = this.pairKey(all[i],all[j]);
        const counts = this.meetings.get(key) ?? {shared:0, partners:0};
        counts.shared++;
        if ((i < 2) === (j < 2)) counts.partners++;
        this.meetings.set(key, counts);
      }
    }
    const now = this.now();
    for (const p of match.teams.flat()) { p.state = 'Ready'; p.readyAt = now; }
    this.history.unshift({...match, winner});
    this.courts[court] = null;
    return true;
  }
  finish() {
    if (this.phase !== 'Live' || this.courts.some(Boolean)) return false;
    this.phase = 'Finished';
    this.ended = this.now();
    return true;
  }
  standings() {
    return this.players.filter(p => p.games).sort((a,b) => b.wins - a.wins
      || b.wins*a.games - a.wins*b.games || b.rating - a.rating || a.checkedAt - b.checkedAt || a.id - b.id);
  }
}
