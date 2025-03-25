const App = () => {
  return (
    <ReactRouterDOM.BrowserRouter>
      <Navbar />
      <ReactRouterDOM.Routes>
        <ReactRouterDOM.Route path="/" element={<Dashboard />} />
        <ReactRouterDOM.Route path="/transactions" element={<TransactionsPage />} />
        <ReactRouterDOM.Route path="/transactions/:hash" element={<TransactionDetail />} />
        <ReactRouterDOM.Route path="/blocks" element={<BlocksPage />} />
        <ReactRouterDOM.Route path="/blocks/:id" element={<BlockDetail />} />
        <ReactRouterDOM.Route path="*" element={<NotFound />} />
      </ReactRouterDOM.Routes>
    </ReactRouterDOM.BrowserRouter>
  );
};

// Render the app
ReactDOM.createRoot(document.getElementById('root')).render(<App />);