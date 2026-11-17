import {PracticeEvent} from './demo-model.mjs';

const byId = id => document.getElementById(id);
const root = byId('demo');
const rules = Object.fromEntries(Object.entries(root.dataset).map(([k,v]) => [k,Number(v)]));
let event;
const el = (tag, text, cls) => {
  const node = document.createElement(tag);
  if (text != null) node.textContent = text;
  if (cls) node.className = cls;
  return node;
};
const action = (label, name, id, primary = false) => {
  const b = el('button',label,'button' + (primary ? ' button-primary' : ''));
  b.type = 'button'; b.dataset.action = name;
  if (id != null) b.dataset.id = id;
  return b;
};
const table = (labels, rows) => {
  const wrap = el('div',null,'table-scroll'), t = el('table',null,'table');
  const header = t.createTHead().insertRow();
  for (const label of labels) { const th = el('th',label); th.scope = 'col'; header.append(th); }
  const body = t.createTBody();
  for (const values of rows) {
    const tr = body.insertRow();
    for (const v of values) tr.insertCell().append(v);
  }
  wrap.append(t);
  return wrap;
};
const names = team => team.map(p => p.name).join(' & ');
function reset() {
  event = null;
  byId('demo-create').reset(); byId('demo-add').reset();
  byId('demo-create').hidden = false; byId('demo-event').hidden = true;
  for (const id of ['demo-actions','demo-phase','demo-board','demo-roster','demo-results','demo-log','demo-log-count','demo-status'])
    byId(id).replaceChildren();
  byId('demo-title').textContent = 'Play Demo';
}
function render(msg) {
  const finished = event.phase === 'Finished', live = event.phase === 'Live';
  byId('demo-title').textContent = event.title;
  byId('demo-create').hidden = true; byId('demo-event').hidden = false;
  byId('demo-phase').textContent = event.phase;
  byId('demo-status').textContent = msg;
  const controls = byId('demo-actions');
  controls.replaceChildren();
  if (!live && !finished) controls.append(action('Start Event','start',null,true));
  if (live) {
    const btn = action('Finish Event','finish');
    btn.disabled = event.courts.some(Boolean);
    controls.append(btn);
  }
  byId('demo-live').hidden = finished;
  byId('demo-results').hidden = !finished;
  if (finished) {
    controls.append(action('Try another event','reset')); renderResults();
  } else {
    const queue = event.queue();
    byId('demo-board').replaceChildren(...event.courts.map((m,i) => {
      const c = el('article',null,'court '+(m ? 'court-busy' : 'court-free'));
      c.append(el('h3','Court '+(i+1)));
      if (!m) {
        c.append(el('p',!live ? 'Ready when the event starts.' : queue.length < 4 ? 'Waiting for four ready players.' : 'Open for the next four.','free-note'));
        const b = action('Assign Next Four','assign',i,true), foot = el('div',null,'court-foot');
        b.disabled = !live || queue.length < 4;
        foot.append(b); c.append(foot);
      } else {
        const teams = el('div',null,'teams');
        m.teams.forEach((ps,t) => {
          if (t) teams.append(el('span','vs','versus'));
          const team = el('div',null,'team'), list = el('ul');
          list.append(...ps.map(p => el('li',p.name)));
          team.append(el('h4','Team '+(t+1)),list); teams.append(team);
        });
        const foot = el('div',null,'court-foot'), row = el('div',null,'row'), cancel = action('Cancel match','cancel',i);
        cancel.classList.add('button-quiet');
        row.append(action('Team 1 Won','win1',i,true),action('Team 2 Won','win2',i,true));
        foot.append(cancel,row); c.append(teams,foot);
      }
      return c;
    }));
    const checkAll = root.querySelector('[data-action="check-all"]');
    checkAll.disabled = !event.players.some(p => p.state === 'Resting');
    const rows = event.players.map(p => {
      const state = p.state === 'Ready' ? 'Queue · '+(queue.indexOf(p)+1) : p.state;
      const buttons = el('div',null,'row');
      if (p.state === 'Playing') buttons.append('On court');
      else {
        buttons.append(action(p.state === 'Ready' ? 'Rest' : 'Check in','toggle',p.id));
        if (p.checkedAt != null && p.state !== 'Left') buttons.append(action('Mark left','leave',p.id));
      }
      return [p.name, state, Math.round(p.rating), buttons];
    });
    byId('demo-roster').replaceChildren(rows.length ? table(['Player','Status','Elo','Action'],rows) : el('p','Add your first player above.','muted'));
  }
  byId('demo-log').replaceChildren(event.history.length ? ledger(event.history) : el('p','No matches recorded yet.','muted'));
  byId('demo-log-count').textContent = event.history.length ? event.history.length+(event.history.length === 1 ? ' match' : ' matches') : '';
}
// The same markup as the finished-event match sheet, so both read alike.
const signed = d => { const n = Math.round(d); return (n < 0 ? '-' : '+')+Math.abs(n); };
function ledger(history) {
  const list = el('ul',null,'match-ledger');
  for (const m of history) {
    const item = el('li',null,m.winner ? null : 'void-match'), label = el('div',null,'match-label');
    label.append(el('h3','Court '+(m.court+1)),el('p','Match #'+m.id));
    if (!m.winner) label.append(el('strong','Cancelled'));
    const teams = el('div',null,'teams');
    m.teams.forEach((ps,t) => {
      if (t) { const vs = el('div','vs','versus'); vs.setAttribute('aria-hidden','true'); teams.append(vs); }
      const won = m.winner === t+1, team = el('div',null,'team'+(won ? ' team-winner' : '')), h = el('h4','Team '+(t+1)), list = el('ul');
      if (won) h.append(' ',el('span','· Won','won'));
      list.append(...ps.map(p => {
        const li = el('li',p.name);
        if (p.delta != null) li.append(' ',el('span',signed(p.delta),'delta'));
        return li;
      }));
      team.append(h,list); teams.append(team);
    });
    item.append(label,teams); list.append(item);
  }
  return list;
}
function eloLine(rating, change) {
  const line = el('span',Math.round(rating)+' Elo ','award-elo');
  line.append(el('b',signed(change)));
  return line;
}
function renderResults() {
  const out = byId('demo-results'), ranked = event.standings();
  out.replaceChildren(el('h2','Event results'));
  const strip = el('dl',null,'recap-strip');
  const elapsed = Math.max(0,Math.floor((event.ended-event.started)/1000));
  const checked = event.players.filter(p => p.checkedAt != null).length;
  for (const [label,value] of [['Matches Played',event.history.filter(m => m.winner).length],['Total Players',checked],['Stayed Till the End',event.players.filter(p => p.checkedAt != null && p.state !== 'Left').length],['Event Length',Math.floor(elapsed/60)+'m '+elapsed%60+'s']]) {
    const cell = el('div'); cell.append(el('dt',label),el('dd',value)); strip.append(cell);
  }
  out.append(strip);
  if (!ranked.length) {out.append(el('p','No completed matches to rank.')); return;}
  const stage = el('section',null,'award-stage'), podium = el('ol',null,'award-podium');
  podium.setAttribute('aria-label','Top three places');
  for (const [i,p] of ranked.slice(0,3).entries()) {
    const item = el('li',null,'award-place place-'+(i+1)), base = el('div',null,'plinth');
    const medal = el('span',null,'place-number'); medal.append(el('b',i+1));
    const record = el('div',null,'award-record');
    record.append(el('strong',p.wins+(p.wins === 1 ? ' win' : ' wins')),el('span',p.games+(p.games === 1 ? ' game' : ' games')+' played'),eloLine(p.rating,p.rating-p.seed));
    base.append(medal,record); item.append(el('h3',p.name),base); podium.append(item);
  }
  stage.append(el('h2','Podium'),podium);
  out.append(stage,table(['Place','Player','Wins','Played','Elo'],ranked.map((p,i) => [i+1,p.name,p.wins,p.games,Math.round(p.rating)])));
}

byId('demo-create').addEventListener('submit', e => {
  e.preventDefault();
  try {event = new PracticeEvent(byId('demo-name').value,Number(byId('demo-courts').value),byId('demo-sample').checked,rules);}
  catch (err) {byId('demo-status').textContent = err.message;return;}
  render('Check players in, then start the event.');
  byId('demo-title').focus();
});
byId('demo-add').addEventListener('submit', e => {
  e.preventDefault();
  try {event.add(byId('demo-player').value,byId('demo-band').value);}
  catch (err) {byId('demo-status').textContent = err.message;return;}
  e.target.reset(); render('Player added to the roster.'); byId('demo-player').focus();
});
root.addEventListener('click', e => {
  const b = e.target.closest('button[data-action]');
  if (!b || !event) return;
  const id = Number(b.dataset.id), op = b.dataset.action;
  let msg;
  switch (op) {
    case 'reset': reset(); byId('demo-name').focus(); return;
    case 'start': event.start();msg = 'Event started. Assign four ready players to an open court.';break;
    case 'finish': if (!event.finish()) return;msg = 'This event is now finished!';break;
    case 'toggle': event.toggle(event.players.find(p => p.id === id));msg = 'Player status updated.';break;
    case 'leave': event.leave(event.players.find(p => p.id === id));msg = 'Player marked left.';break;
    case 'check-all': event.checkAll();msg = 'All resting players joined the queue.';break;
    case 'assign': if (!event.assign(id)) return;msg = 'Four players assigned to Court '+(id+1)+'.';break;
    case 'win1': case 'win2': case 'cancel':
      if (!event.record(id,op === 'cancel' ? 0 : op === 'win1' ? 1 : 2)) return;
      msg = op === 'cancel' ? 'Match cancelled. Elo is unchanged.' : 'Winner recorded. Elo and the queue are updated.';
      break;
    default: return;
  }
  render(msg);
  // Rendering replaces the court and roster buttons; return keyboard focus
  // to the corresponding control or to the event heading when the phase changes.
  const same = [...root.querySelectorAll('button[data-action]:not(:disabled)')].find(n =>
    n.dataset.action === op && n.dataset.id === b.dataset.id && n.getClientRects().length);
  const court = ['assign','win1','win2','cancel'].includes(op) && byId('demo-board').children[id];
  (same || (court && court.querySelector('button:not(:disabled)')) || byId('demo-title')).focus({preventScroll:true});
});
reset();
window.addEventListener('pageshow', e => {if (e.persisted) reset();});
