import { useState, useEffect } from 'react'
import { core } from './lib/core'
import { listen } from '@tauri-apps/api/event'
import './App.css'

function App() {
  const [output, setOutput] = useState<string>('')
  const [events, setEvents] = useState<string[]>([])

  useEffect(() => {
    const unlisten1 = listen('core://file_added', (e) => {
      setEvents(prev => [...prev, `Added: ${JSON.stringify(e.payload)}`]);
    });
    const unlisten2 = listen('core://file_changed', (e) => {
      setEvents(prev => [...prev, `Changed: ${JSON.stringify(e.payload)}`]);
    });
    
    return () => {
      unlisten1.then(f => f());
      unlisten2.then(f => f());
    }
  }, []);

  const testDoctor = async () => {
    try {
      const res = await core.doctor();
      console.log('Doctor result:', res);
      setOutput(JSON.stringify(res, null, 2));
    } catch (e: any) {
      console.error(e);
      setOutput(`Error: ${e.message}`);
    }
  }

  const testLs = async () => {
    try {
      const res = await core.ls('');
      console.log('Ls result:', res);
      setOutput(JSON.stringify(res, null, 2));
    } catch (e: any) {
      console.error(e);
      setOutput(`Error: ${e.message}`);
    }
  }

  return (
    <main className="container" style={{ padding: '2rem' }}>
      <h1>Symaira Desktop - Phase 4</h1>
      <div style={{ display: 'flex', gap: '1rem', marginBottom: '1rem' }}>
        <button onClick={testDoctor}>
          Run `symdesk doctor`
        </button>
        <button onClick={testLs}>
          Run `symdesk ls`
        </button>
      </div>
      <pre style={{ textAlign: 'left', background: '#222', color: '#fff', padding: '1rem', borderRadius: '4px', overflow: 'auto', maxHeight: '200px' }}>
        {output || 'Click a button to test the CoreClient SDK.'}
      </pre>
      <h2>Event Log</h2>
      <pre style={{ textAlign: 'left', background: '#222', color: '#fff', padding: '1rem', borderRadius: '4px', overflow: 'auto', maxHeight: '200px' }}>
        {events.join('\n') || 'No events yet...'}
      </pre>
    </main>
  )
}

export default App
